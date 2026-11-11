package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	tempPath          = "/sys/class/thermal/thermal_zone0/temp"
	vcioPath          = "/dev/vcio"
	tagGetThrottled   = 0x00030046 // firmware mailbox tag
	ioctlMboxProperty = 0xC0086400 // _IOWR(100, 0, char*) on arm64
	annPrefix         = "thermal.stellat.dev/"
)

// Température SoC en °C (sysfs, en millidegrés)
func readTempC() (float64, error) {
	b, err := os.ReadFile(tempPath)
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return float64(m) / 1000, nil
}

// Flags de throttling via la mailbox firmware (/dev/vcio)
func readThrottled() (uint32, error) {
	f, err := os.OpenFile(vcioPath, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var buf [8]uint32
	buf[0] = uint32(len(buf) * 4) // taille totale
	buf[1] = 0                    // requête
	buf[2] = tagGetThrottled      // tag
	buf[3] = 4                    // taille de la valeur
	buf[4] = 0                    // indicateur requête
	buf[5] = 0                    // valeur
	buf[6] = 0                    // fin
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		ioctlMboxProperty, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return 0, errno
	}
	if buf[1] != 0x80000000 {
		return 0, fmt.Errorf("mailbox error: 0x%x", buf[1])
	}
	return buf[5], nil
}

func main() {
	interval := flag.Duration("interval", 15*time.Second, "période de mesure")
	dryRun := flag.Bool("dry-run", false, "affiche sans écrire dans Kubernetes")
	once := flag.Bool("once", false, "une seule mesure puis quitte")
	flag.Parse()

	node := os.Getenv("NODE_NAME")
	var cs *kubernetes.Clientset
	if !*dryRun {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			log.Fatalf("config k8s: %v", err)
		}
		cs = kubernetes.NewForConfigOrDie(cfg)
		if node == "" {
			log.Fatal("NODE_NAME vide")
		}
	}

	for {
		temp, errT := readTempC()
		thr, errM := readThrottled()
		if errT != nil || errM != nil {
			log.Printf("erreur lecture: temp=%v mailbox=%v", errT, errM)
		} else {
			ann := map[string]string{
				annPrefix + "temp-celsius":  fmt.Sprintf("%.1f", temp),
				annPrefix + "throttled":     fmt.Sprintf("0x%x", thr),
				annPrefix + "throttled-now": strconv.FormatBool(thr&0xF != 0),
				annPrefix + "updated":       time.Now().UTC().Format(time.RFC3339),
			}
			if *dryRun {
				log.Printf("node=%q %v", node, ann)
			} else {
				patch, _ := json.Marshal(map[string]any{
					"metadata": map[string]any{"annotations": ann}})
				_, err := cs.CoreV1().Nodes().Patch(context.Background(), node,
					types.MergePatchType, patch, metav1.PatchOptions{})
				if err != nil {
					log.Printf("patch node: %v", err)
				}
			}
		}
		if *once {
			return
		}
		time.Sleep(*interval)
	}
}
