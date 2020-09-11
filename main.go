package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudflare/cloudflare-go"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"net"
	"os"
	"strings"
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
	Host    string
	IP      []string
	Pod     string
	Proxied bool
}

var (
	dryRun = false
)

func main() {
	client := NewClient(NewKubernetesClient(), NewCloudflareClient())

	pods, err := client.retrieveInitialDNS(context.Background())
	if err != nil { // need to be able to retrieve initial DNS
		panic(err)
	}

	// do initial DNS update
	client.updateDns(pods)

	for {
		if err := client.watchUpdates(); err != nil {
			fmt.Printf("Error watching for updates: %e", err)
		}
		// TODO: Retrieve initial DNS again?
	}
}

func (c *Client) retrieveInitialDNS(ctx context.Context) ([]PodDNS, error) {
	var nodeSet []NodeSet
	var podDNSToSet []PodDNS

	nodes, err := c.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	//fmt.Println("Current Node IPs:")

	for _, node := range nodes.Items {
		publicIp := getPublicIp(&node)
		nodeSet = append(nodeSet, NodeSet{Name: node.Name, IP: publicIp})
		//fmt.Printf("%s external ip is %s\n", node.Name, publicIp)
	}

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "metrics"
	}

	pods, err := c.kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, pod := range pods.Items {
		appName := pod.Labels["app"]
		if pod.Annotations["tombowdit.ch/node-dns"] == "" {
			continue
		}

		nodeIp := ""
		for _, node := range nodeSet {
			if pod.Spec.NodeName == node.Name {
				nodeIp = node.IP
			}
		}

		if nodeIp == "" {
			fmt.Println("Fatal error - no node IP found")
			continue
		}

		fmt.Printf("We have pod %s app %s that needs to have DNS of %s that is currently on %s (%s)\n", pod.Name, appName, pod.Annotations["tombowdit.ch/node-dns"], pod.Spec.NodeName, nodeIp)

		existing := false

		var newDns []PodDNS
		for _, pd := range podDNSToSet {
			if pd.Host == pod.Annotations["tombowdit.ch/node-dns"] {
				existing = true
				// add ip but make sure it isn't duplicated
				_, found := find(pd.IP, nodeIp)
				if !found {
					pd.IP = append(pd.IP, nodeIp)
				}

				newDns = append(newDns, pd)
			} else {
				newDns = append(newDns, pd)
			}
		}

		if !existing {
			podDNSToSet = append(podDNSToSet, createPodDns(&pod, nodeIp))
		}
	}

	return podDNSToSet, nil
}

func (c *Client) watchUpdates() error {
	client := c.kubernetes

	watcher, err := client.CoreV1().Pods(os.Getenv("NAMESPACE")).Watch(context.Background(), metav1.ListOptions{
		Watch: true,
	})
	if err != nil {
		return err
	}

	for ev := range watcher.ResultChan() {
		switch ev.Type {
		case watch.Added:
			fallthrough
		case watch.Modified:
			dns, err := c.retrieveInitialDNS(context.Background())
			if err != nil {
				fmt.Printf("Error retrieving new DNS: %e", err)
				continue
			}

			c.updateDns(dns)
		case watch.Deleted:
			if pod, ok := ev.Object.(*v1.Pod); ok {
				if host, ok := pod.Annotations["tombowdit.ch/node-dns"]; ok {
					// get public ip for pod
					node, err := c.getNode(pod)
					if err != nil {
						fmt.Printf("Error getting node for pod %s: %e", pod.Name, err)
						continue
					}

					ip := getPublicIp(node)
					if ip == "" {
						continue
					}

					zoneMap, err := c.getZoneMap()
					if err != nil {
						fmt.Printf("Error getting zones: %e", err)
						continue
					}

					zone, err := (&PodDNS{Host: host}).getZone(zoneMap) // TODO: Clean this up
					if err != nil {
						fmt.Printf("Error getting zone for pod %s: %e", pod.Name, err)
						continue
					}

					recordType, err := getRecordType(ip)
					if err != nil {
						fmt.Printf("Error getting record type for %s: %e", ip, err)
						continue
					}

					// delete records
					records, err := c.cloudflare.DNSRecords(zone.ID, cloudflare.DNSRecord{
						Type:       string(recordType),
						Name:       host,
						Content:    ip,
					})
					if err != nil {
						fmt.Printf("Error getting records to delete (%s:%s): %e", host, ip, err)
						continue
					}

					// we should only delete 1 record, as if there are multiple, there must be other remaining pods
					// with the same annotation on the node

					if len(records) > 0 {
						if err := c.cloudflare.DeleteDNSRecord(zone.ID, records[0].ID); err != nil {
							fmt.Printf("Error deleting record (%s:%s): %e", host, ip, err)
							continue
						}
					}
				}
			}
		}
	}

	return nil
}

func createPodDns(pod *v1.Pod, nodeIp string) PodDNS {
	var proxied bool
	if value, ok := pod.Annotations["tombowdit.ch/cloudflare-proxy"]; ok {
		proxied = value == "true"
	}

	return PodDNS{
		Host:    pod.Annotations["tombowdit.ch/node-dns"],
		IP:      []string{nodeIp},
		Pod:     pod.Name,
		Proxied: proxied,
	}
}

type RecordType string

const (
	RecordA    RecordType = "A"
	RecordAAAA RecordType = "AAAA"
)

func getRecordType(ip string) (RecordType, error) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", errors.New(fmt.Sprintf("Invalid IP address: %s", ip))
	}

	if parsed.To4() != nil {
		return RecordA, nil
	}

	if parsed.To16() != nil {
		return RecordAAAA, nil
	}

	return "", errors.New(fmt.Sprintf("Invalid IP address: %s", ip))
}

func (c *Client) updateDns(dns []PodDNS) {
	api := c.cloudflare

	// zone_id -> [record name -> []record]
	records := make(map[string]map[string][]cloudflare.DNSRecord)

	zones, err := c.getZoneMap()
	if err != nil {
		fmt.Printf("Failed to get zones: %e", err)
		return
	}

	for _, zone := range zones {
		records[zone.ID] = make(map[string][]cloudflare.DNSRecord)
	}

	for _, pod := range dns {
		zone, err := pod.getZone(zones)
		if err != nil {
			fmt.Println(err.Error())
			continue
		}

		var existingRecords []cloudflare.DNSRecord
		if r, ok := records[zone.ID][pod.Host]; ok {
			existingRecords = r
		} else {
			r, err := api.DNSRecords(zone.ID, cloudflare.DNSRecord{Name: pod.Host})
			if err != nil {
				fmt.Printf("Couldn't find existing records for %s, skipping\n", pod.Host)
				continue
			}

			records[zone.ID][pod.Host] = r
			existingRecords = r
		}

		var activeIps []string // slice of IPs already active on cloudflare
		for _, record := range existingRecords {
			if record.Type != "A" && record.Type != "AAAA" {
				continue
			}

			if record.Name != pod.Host {
				continue
			}

			// check if the record's IP is an IP that we no longer have
			if _, found := find(pod.IP, record.Content); found {
				activeIps = append(activeIps, record.Content)
				continue
			}

			fmt.Printf("Deleting DNS record (reason: not in IP list %s) %s:%s\n", pod.IP, record.Name, record.Content)

			if !dryRun {
				if err := api.DeleteDNSRecord(zone.ID, record.ID); err != nil {
					fmt.Printf("Error deleting record %s:%s: %e\n", record.Name, record.Content, err)
					continue
				}
			}
		}

		// add pod IPs to cloudflare if they arent already
		for _, podIp := range pod.IP {
			if _, found := find(activeIps, podIp); found {
				continue
			}

			recordType, err := getRecordType(podIp)
			if err != nil {
				fmt.Printf("Invalid record type for %s: %s", podIp, err)
				continue
			}

			fmt.Printf("Creating record for %s (%s)\n", podIp, pod.Host)

			if !dryRun {
				_, err = api.CreateDNSRecord(zone.ID, cloudflare.DNSRecord{
					Type:    string(recordType),
					Name:    pod.Host,
					Content: podIp,
					Proxied: pod.Proxied,
					TTL:     1,
				})

				if err == nil {
					fmt.Printf("Created record for %s:%s\n", pod.Host, podIp)
				} else {
					fmt.Printf("Error creating DNS record for %s (%s / %s): %e\n", pod.Pod, pod.Host, podIp, err)
				}
			}
		}
	}
}

func (pod *PodDNS) getZone(zones map[string]cloudflare.Zone) (podZone cloudflare.Zone, err error) {
	for _, zone := range zones {
		if strings.HasSuffix(pod.Host, zone.Name) {
			podZone = zone
		}
	}

	if podZone.Name == "" {
		err = errors.New(fmt.Sprintf("No zone found for %s", pod.Host))
	}

	return
}

func (c *Client) getZoneMap() (map[string]cloudflare.Zone, error) {
	zoneMap := make(map[string]cloudflare.Zone)

	zones, err := c.cloudflare.ListZones()
	if err != nil {
		return zoneMap, err
	}

	for _, zone := range zones {
		zoneMap[zone.ID] = zone
	}

	return zoneMap, nil
}

func (c *Client) getNode(pod *v1.Pod) (*v1.Node, error) {
	nodes, err := c.kubernetes.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, node := range nodes.Items {
		if node.Name == pod.Spec.NodeName {
			return &node, nil
		}
	}

	return nil, nil
}

func getPublicIp(node *v1.Node) (publicIp string) {
	for _, address := range node.Status.Addresses {
		if address.Type == "ExternalIP" {
			publicIp = address.Address
			break
		}
	}
	return
}

func find(slice []string, val string) (int, bool) {
	for i, item := range slice {
		if item == val {
			return i, true
		}
	}
	return -1, false
}
