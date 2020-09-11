package main

import (
	"flag"
	"fmt"
	"github.com/cloudflare/cloudflare-go"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"path/filepath"
)

type Client struct {
	kubernetes *kubernetes.Clientset
	cloudflare *cloudflare.API
}

func NewClient(kubernetes *kubernetes.Clientset, cloudflare *cloudflare.API) *Client {
	return &Client{
		kubernetes: kubernetes,
		cloudflare: cloudflare,
	}
}

func NewKubernetesClient() (cs *kubernetes.Clientset) {
	if os.Getenv("TYPE") != "outside" {
		fmt.Println("using inside credentials")
		config, err := rest.InClusterConfig()

		if err != nil {
			panic(err.Error())
		}

		// creates the clientset
		cs, err = kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}
	} else {
		fmt.Println("using kubeconfig (outside) credentials")

		var kubeConfig string
		if home := homeDir(); home != "" {
			flag.StringVar(&kubeConfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			flag.StringVar(&kubeConfig, "kubeconfig", "", "absolute path to the kubeconfig file")
		}

		flag.Parse()

		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			panic(err.Error())
		}

		// create the clientSet
		cs, err = kubernetes.NewForConfig(config)
	}

	return
}

func NewCloudflareClient() *cloudflare.API {
	cfApi, err := cloudflare.NewWithAPIToken(os.Getenv("CF_API_TOKEN"))
	if err != nil {
		panic(err)
	}

	return cfApi
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
