package main


import (
	"context"
	"flag"
	"fmt"
	"log"
//	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ericchiang/k8s"
//	"github.com/ericchiang/k8s/api/v1"
//	metav1 "github.com/ericchiang/k8s/apis/meta/v1"

	"encoding/json"
	"io/ioutil"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/ghodss/yaml"
	"sort"
	"strings"
	"errors"
)

type WatchEvent struct {
	Type   string          `json:"type,omitempty" description:"the type of watch event; may be ADDED, MODIFIED, DELETED, or ERROR"`
	Object json.RawMessage `json:"object,omitempty" description:"the object being watched; will match the type of the resource endpoint or be a Status object if the type is ERROR"`
}

type Metadata struct {
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
}

type Service struct {
	ApiVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   Metadata      `json:"metadata"`
	Status     ServiceStatus `json:"status"`
}

type ServiceStatus struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
}

type LoadBalancer struct {
	Ingress []Ingress `json:"ingress"`
}

type Ingress struct {
	Hostname string `json:"hostname"`
}

// Arguments
var (
	argKubecfgFile          = flag.String("kubecfg-file", "", "Location of kubecfg file for access to kubernetes.")
	argKubeMasterURL        = flag.String("kube-master-url", "", "URL to reach kubernetes master. " +
		"Env variables in this flag will be expanded.")

	argV                    = flag.String("v", "1", "Log level")


	k8sClient   *k8s.Client
)

func exitWithError(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// loadClient parses a kubeconfig from a file and returns a Kubernetes
// client. It does not support extensions or client auth providers.
func loadClient(kubeconfigPath string) (*k8s.Client, error) {
	data, err := ioutil.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %v", err)
	}

	// Unmarshal YAML into a Kubernetes config object.
	var config k8s.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("unmarshal kubeconfig: %v", err)
	}
	return k8s.NewClient(&config)
}

func main() {

	flag.Parse()

	var err error
	var client *k8s.Client

	// Create a Kubernetes client.
	if argKubecfgFile != nil || *argKubecfgFile != ""  {
		client, err = loadClient(*argKubecfgFile)
	} else {
		client, err = k8s.NewInClusterClient()
	}

	if err != nil {
		log.Fatal(err)
		//exitWithError(err)
	}



	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svcs, err := client.CoreV1().ListServices(ctx, k8s.AllNamespaces)
	if err != nil {
		exitWithError(err)
	}

	resourceVersion := svcs.Metadata.ResourceVersion
	log.Printf("Resource version: %+v", *resourceVersion)

	w, err := client.CoreV1().WatchServices(ctx, k8s.AllNamespaces, k8s.ResourceVersion(*resourceVersion))
	if err != nil {
		exitWithError(err)
	}
	defer w.Close()

	go func() {
		for {
			time.Sleep(10 * time.Second)
			if event, gotFromWatch, err := w.Next(); err != nil {
				log.Printf("expected event type %q got %q %q", "EventAdded", gotFromWatch, *event.Type)
			} else {
				if *event.Type == k8s.EventAdded {
					domainName := gotFromWatch.GetMetadata().GetAnnotations()["domainName"]
					if ingress:= gotFromWatch.Status.LoadBalancer.Ingress; domainName != "" && len(ingress) > 0 && *ingress[0].Hostname != "" {
						elb := ingress[0].Hostname
						log.Printf("Domain added: %+v", domainName)
						hZ, err := GetHostedZonesByName(&domainName)
						if err != nil {
							log.Println("Can't find hosted zone for domain %q", domainName)
							continue
						}
						UpdateRoute53(hZ, &domainName, elb)

					}
				} else if *event.Type == k8s.EventDeleted {
					domainName := gotFromWatch.GetMetadata().GetAnnotations()["domainName"]
					if domainName != "" {
						log.Printf("Domain deleted: %+v", domainName)
						hZ, err := GetHostedZonesByName(&domainName)
						if err != nil {
							log.Println("Can't find hosted zone for domain %q", domainName)
							continue
						}
						DeleteRoute53(hZ, &domainName)
					}
				} else if *event.Type == k8s.EventModified {
					domainName := gotFromWatch.GetMetadata().GetAnnotations()["domainName"]
					if ingress:= gotFromWatch.Status.LoadBalancer.Ingress; domainName != "" && len(ingress) > 0 && *ingress[0].Hostname != "" {
						elb := ingress[0].Hostname
						log.Printf("Domain modified: %+v", domainName)
						hZ, err := GetHostedZonesByName(&domainName)
						if err != nil {
							log.Println("Can't find hosted zone for domain %q", domainName)
							continue
						}
						UpdateRoute53(hZ, &domainName, elb)

					}
				}
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("Shutdown signal received, exiting...")
}

/*
func ProcessELB(gotFromWatch *k8s.CoreV1ServiceWatcher) {
	domainName := gotFromWatch.GetMetadata().GetAnnotations()["domainName"]
	elb := gotFromWatch.Status.LoadBalancer.Ingress[0].Hostname
	if domainName != "" && *elb != "" {
		log.Printf("Domain added: %+v", domainName)
		hZ, err := GetHostedZonesByName(&domainName)
		if err != nil {
			log.Println("Can't find hosted zone for domain %q", domainName)
			return
		}
		UpdateRoute53(hZ, &domainName, elb)

	}
} */


func GetHostedZonesByName(domainName *string)  (*route53.HostedZone, error) {
	sess := session.Must(session.NewSession())

	svc := route53.New(sess)

	params := &route53.ListHostedZonesByNameInput{
	}
	resp, err := svc.ListHostedZonesByName(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return nil, err
	}

	// Pretty-print the response data.
	fmt.Println(resp)

	matching := sort.Search(len(resp.HostedZones), func(z int) bool { return strings.HasSuffix(*domainName + ".",
		*resp.HostedZones[z].Name) })

	if matching < len(resp.HostedZones){
		return resp.HostedZones[matching], nil
	} else {
		// x is not present in data,
		// but i is the index where it would be inserted.
		return nil, errors.New("Can't find the ho")
	}
}

func UpdateRoute53(hostedZone *route53.HostedZone, domainName *string, elb *string ) {
	sess := session.Must(session.NewSession())

	svc := route53.New(sess)

	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String("UPSERT"),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name: aws.String(*domainName), // Required
						Type: aws.String("CNAME"),
						ResourceRecords: []*route53.ResourceRecord{
							{
								Value: aws.String(*elb), // Required
							},

						},
						TTL:           aws.Int64(300),


					},
				},
				// More values...
			},
			Comment: aws.String("ResourceDescription"),
		},
		HostedZoneId: aws.String(*hostedZone.Id), // Required
	}
	resp, err := svc.ChangeResourceRecordSets(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return
	}

	// Pretty-print the response data.
	fmt.Println(resp)
}

func DeleteRoute53(hostedZone *route53.HostedZone, domainName *string) {
	sess := session.Must(session.NewSession())

	svc := route53.New(sess)

	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{ // Required
			Changes: []*route53.Change{ // Required
				{ // Required
					Action: aws.String("DELETE"), // Required
					ResourceRecordSet: &route53.ResourceRecordSet{ // Required
						Name: aws.String(*domainName), // Required
						Type: aws.String("CNAME"),  // Required
					},
				},
				// More values...
			},
			Comment: aws.String("ResourceDescription"),
		},
		HostedZoneId: aws.String(*hostedZone.Id), // Required
	}
	resp, err := svc.ChangeResourceRecordSets(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		fmt.Println(err.Error())
		return
	}

	// Pretty-print the response data.
	fmt.Println(resp)
}
