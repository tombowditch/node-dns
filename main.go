package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type DNSSet struct {
	App string
	DNS string
}

type NodeSet struct {
	Name string
	IP   string
}

type PodDNS struct {
	Host string
	IP   []string
	Pod  string
}

var (
	dryrun = true
)

func main() {
	var clientset *kubernetes.Clientset

	ctx := context.Background()

	if os.Getenv("TYPE") != "outside" {
		fmt.Println("using inside credentials")
		config, err := rest.InClusterConfig()

		if err != nil {
			panic(err.Error())
		}
		// creates the clientset
		cs, err := kubernetes.NewForConfig(config)

		if err != nil {
			panic(err.Error())
		}

		clientset = cs
	} else {
		fmt.Println("using kubeconfig (outside) credentials")
		var kubeconfig *string

		if home := homeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()

		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}

		// create the clientset
		cs, err := kubernetes.NewForConfig(config)
		clientset = cs
	}

	cfapi, err := cloudflare.NewWithAPIToken(os.Getenv("CF_API_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	// loop every 30s
	for {
		pdns := getNeededDNS(clientset, ctx)

		x, _ := json.Marshal(pdns)
		fmt.Println(string(x))

		for _, dns := range pdns {
			zones, err := cfapi.ListZones()
			if err != nil {
				panic(err)
			}

			var myZone cloudflare.Zone

			for _, zone := range zones {
				if strings.HasSuffix(dns.Host, zone.Name) {
					myZone = zone
				}
			}

			if myZone.Name == "" {
				fmt.Printf("ERROR - no zone found for %s\n", dns.Host)
				continue
			}

			existingRecords, err := cfapi.DNSRecords(myZone.ID, cloudflare.DNSRecord{Name: dns.Host})
			if err != nil {
				fmt.Printf("ERROR - can not get existing records for %s\n", dns.Host)
				continue
			}

			cfIPs := []string{}

			for _, record := range existingRecords {
				recordJson, _ := json.Marshal(record)

				// check if it is A or AAAA, else discard
				if record.Type != "A" && record.Type != "AAAA" {
					continue
				}

				if record.Name != dns.Host {
					continue
				}

				cfIPs = append(cfIPs, record.Content)

				// check if a ip is there we don't have
				_, found := Find(dns.IP, record.Content)
				if !found {
					fmt.Printf("Deleting DNS record (reason: not in IP list %s) %s\n", dns.IP, string(recordJson))

					if !dryrun {
						err := cfapi.DeleteDNSRecord(myZone.ID, record.ID)
						if err != nil {
							fmt.Println("ERROR - could not delete dns record: " + err.Error())
						}
					}
					continue
				}

			}

			// loop current IPs
			for _, localrecord := range dns.IP {
				// check that it is in
				_, found := Find(cfIPs, localrecord)
				if !found {
					dnsType := "A"
					if strings.Contains(localrecord, ":") {
						dnsType = "AAAA"
					}

					dr := cloudflare.DNSRecord{Name: dns.Host, Type: dnsType, Content: localrecord}
					fmt.Printf("Adding DNS record (reason: not on DNS) %+v\n", dr)
					if !dryrun {
						resp, err := cfapi.CreateDNSRecord(myZone.ID, dr)

						if err != nil {
							fmt.Println("ERROR - could not add dns record: " + err.Error())
						} else {
							aj, _ := json.Marshal(resp)
							fmt.Printf("Added DNS record: %s\n", string(aj))
						}
					}
				}
			}

		}

		// sleep 30s
		fmt.Println("Sleeping 30s...")
		time.Sleep(time.Second * 30)
	}
}

func getNeededDNS(clientset *kubernetes.Clientset, ctx context.Context) []PodDNS {
	nodeSet := []NodeSet{}
	podDNSToSet := []PodDNS{}

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		panic(err)
	}

	fmt.Println("Current Node IPs:")

	for _, node := range nodes.Items {
		pubIP := ""
		for _, address := range node.Status.Addresses {
			if address.Type == "ExternalIP" {
				pubIP = address.Address
			}
		}

		nodeName := node.Name

		nodeSet = append(nodeSet, NodeSet{Name: nodeName, IP: pubIP})

		fmt.Printf("%s external ip is %s\n", nodeName, pubIP)

	}

	fmt.Println("")

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "metrics"
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})

	for _, pod := range pods.Items {
		appName := pod.Labels["app"]

		if pod.Annotations["tombowdit.ch/node-dns"] == "" {
			continue
		}

		nodeIP := ""
		for _, node := range nodeSet {
			if pod.Spec.NodeName == node.Name {
				nodeIP = node.IP
			}
		}

		if nodeIP == "" {
			fmt.Println("Fatal error - no node IP found")
			continue
		}

		fmt.Printf("We have pod %s app %s that needs to have DNS of %s that is currently on %s (%s)\n", pod.Name, appName, pod.Annotations["tombowdit.ch/node-dns"], pod.Spec.NodeName, nodeIP)

		existing := false

		nl := []PodDNS{}

		for _, pd := range podDNSToSet {
			if pd.Host == pod.Annotations["tombowdit.ch/node-dns"] {
				existing = true
				// add ip but make sure it isn't duplicated

				_, found := Find(pd.IP, nodeIP)
				if !found {
					pd.IP = append(pd.IP, nodeIP)
				}
				// append
				nl = append(nl, pd)
			} else {
				// append
				nl = append(nl, pd)

			}
		}

		if !existing {
			podDNSToSet = append(podDNSToSet, PodDNS{Host: pod.Annotations["tombowdit.ch/node-dns"], IP: []string{nodeIP}, Pod: pod.Name})
		}

	}

	return podDNSToSet
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func Find(slice []string, val string) (int, bool) {
	for i, item := range slice {
		if item == val {
			return i, true
		}
	}
	return -1, false
}
