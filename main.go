package main // import "github.com/spaaza/route53-registrator"

import (
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/service/route53"
	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/golang/glog"
	"io"
)

func getopt(name, def string) string {
	if env := os.Getenv(name); env != "" {
		return env
	}
	return def
}

func assert(err error) {
	if err != nil {
		glog.Error(err)
	}
}

func recordExists(client *route53.Route53, zoneId string, zoneName string, value string) (exists bool, err error) {
	matchingResourceRecords, err := findMatchingResourceRecordsByName(client, zoneId, zoneName)
	exists = false
	for _, recordSet := range matchingResourceRecords {
		for _, record := range recordSet.ResourceRecords {
			if *record.Value == value {
				glog.Infof("Found existing record with Name %s and value %s.", zoneName, value)
				exists = true
			}
		}
	}
	return exists, nil
}

//uses the ec2 metadata service to retrieve the private
//IP for the instance
func hostname(metadataServerAddress string) (hostname string) {
	host := []string{"http:/", metadataServerAddress, "latest", "meta-data", "local-ipv4"}
	resp, err := http.Get(strings.Join(host, "/"))
	assert(err)

	defer resp.Body.Close()
	assert(err)
	body, err := ioutil.ReadAll(resp.Body)
	assert(err)
	return string(body)
}

//container names start with a /. This function adds the leading / if it doesn't exist.
func normalizedContainerName(original string) (normalized string) {
	if strings.HasPrefix(original, "/") {
		return original
	}
	return strings.Join([]string{"/", original}, "")
}

func ecsContainerFamilyLabel(client *dockerapi.Client, containerId string) (ecsContainerLabel string) {
	container, err := client.InspectContainer(containerId)
	if err != nil {
		glog.Error(err)
	} else if ecsContainerLabel, ok := container.Config.Labels["com.amazonaws.ecs.task-definition-family"]; ok {
		return ecsContainerLabel
	}
	return "";
}

func ecsContainerNameLabel(client *dockerapi.Client, containerId string) (ecsContainerLabel string) {
	container, err := client.InspectContainer(containerId)
	if err != nil {
		glog.Error(err)
	} else if ecsContainerLabel, ok := container.Config.Labels["com.amazonaws.ecs.container-name"]; ok {
		return ecsContainerLabel
	}
	return "";
}

//Given a container label check whether we should observe it for registrations and deletions
func isObservedContainer(ecsContainerLabel string) (observed bool) {
	if strings.HasSuffix(ecsContainerLabel, "-service") { // a convention
		return true
	}
	glog.Infof("Container label: %s is not a service that should be registered", ecsContainerLabel)
	return false
}

//Find all resource records in a AWS Hosted Zone that match a given name.
func findMatchingResourceRecordsByName(client *route53.Route53, zone string, setName string) (recordSet []*route53.ResourceRecordSet, err error) {
	resources, err := client.ListResourceRecordSets(&route53.ListResourceRecordSetsInput{
		HostedZoneID: aws.String(zone),
	})
	if awserr, ok := err.(awserr.Error); ok {
		// A service error occurred.
		glog.Errorf("AWS Error: \n Code: %s \n Message: %s \n awsErr.OrigErr: %v", awserr.Code(), awserr.Message(), awserr.OrigErr())
		return nil, err
	} else if err != nil {
		// A non-service error occurred.
		return nil, err
	}
	var matching []*route53.ResourceRecordSet
	for _, route := range resources.ResourceRecordSets {
		//FQDNs have a trailing dot. Check if that which has been provided
		//matches the route, irrespective of the trailing dot
		if strings.TrimRight(*route.Name, ".") == strings.TrimRight(setName, ".") {
			matching = append(matching, route)
		}
	}
	return matching, nil
}

//Creates a ResourceRecordSet with a default TTL and Weight.
//The SetIdentifier equals the the hostname of the server.
func WeightedResourceRecordSetForValue(zoneName string, value string) (resourceRecordSet *route53.ResourceRecordSet) {
	return &route53.ResourceRecordSet{
		Name: aws.String(zoneName),
		Type: aws.String("A"),
		ResourceRecords: []*route53.ResourceRecord{
			&route53.ResourceRecord{
				Value: aws.String(value),
			},
		},
		SetIdentifier: aws.String(value),
		TTL:           aws.Long(5),
		Weight:        aws.Long(50),
	}
}

//Creates the necessary params for a ChangeResourceRecordRequest
func paramsForChangeResourceRecordRequest(client *route53.Route53, action string, zoneId string, resourceRecordSet *route53.ResourceRecordSet) route53.ChangeResourceRecordSetsInput {
	changes := []*route53.Change{&route53.Change{
		Action:            aws.String(action),
		ResourceRecordSet: resourceRecordSet,
	}}
	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: changes,
		},
		HostedZoneID: aws.String(zoneId),
	}
	return *params
}

//Defines a Route53 request for a weighted resource record set
type requestFn func(client *route53.Route53, action string, zoneId string, zoneName string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error)

//Executes the ChangeResourceRecordSet
func route53ChangeRequest(client *route53.Route53, action string, zoneId string, zoneName string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error) {
	resourceRecordSet := WeightedResourceRecordSetForValue(zoneName, value)
	params := paramsForChangeResourceRecordRequest(client, action, zoneId, resourceRecordSet)
	return client.ChangeResourceRecordSets(&params)
}

//A closure that consumes a requestFn as a parameter
//and returns a requestFn that handles errors resulting
//from it's execution
func ErrorHandledRequestFn(reqFn requestFn) (wrapped requestFn) {
	return func(route53Client *route53.Route53, action string, zoneId string, zoneName string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error) {
		resp, err = reqFn(route53Client, action, zoneId, zoneName, value)
		if awserr, ok := err.(awserr.Error); ok {
			glog.Errorf("AWS Error: \n Code: %s \n Message: %s \n awsErr.OrigErr(): %v", awserr.Code(), awserr.Message(), awserr.OrigErr())
			return nil, err
		} else if err != nil {
			// A non-service error occurred.
			glog.Errorf("Error occured creating records: \n %s", err)
			return nil, err
		}
		glog.Infof("Response received for request: \n %s", awsutil.StringValue(resp))
		return resp, nil
	}
}

//Specifies a type of function used to dispatch
type requestFnForZoneClient func(action string, zoneName string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error)

func requestFnForClientZone(client *route53.Route53, zoneId string, fn requestFn) (curried requestFnForZoneClient) {
	return func(action string, zoneName string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error) {
		return fn(client, action, zoneId, zoneName, value)
	}
}

func main() {
	var metadataIP = flag.String("metadata", "169.254.169.254", "The address of the metadata service")
	var region = flag.String("region", "us-east-1", "The region for route53 records")
	var zoneId = flag.String("zone", "Z1P7DHMHEAX6O3", "The route53 hosted zone id")
	var listenAddr = flag.String("listenAddr", ":12000", "Address for HTTP listener")

	//Print some debug information
	flag.Parse()
	glog.Info(*region)
	glog.Info(*metadataIP)
	glog.Info(*zoneId)

	docker, err := dockerapi.NewClient(getopt("DOCKER_HOST", "unix:///tmp/docker.sock"))
	assert(err)
	err = docker.Ping()
	assert(err)

	events := make(chan *dockerapi.APIEvents)
	assert(docker.AddEventListener(events))
	client := route53.New(nil)

	// Start a web server so that we can bind a port on ECS to ensure we get a registrator per instance in the cluster.
	http.HandleFunc("/", func (w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "OK")
	})
	go http.ListenAndServe(*listenAddr, nil)

	weightedResourceRecordSetFn := ErrorHandledRequestFn(route53ChangeRequest)
	weightedRequestForClientZone := requestFnForClientZone(client, *zoneId, weightedResourceRecordSetFn)

	glog.Infof("Listening for Docker events ...")

	// Process Docker events
	for msg := range events {
		ecsContainerLabel := ecsContainerFamilyLabel(docker, msg.ID)
		zoneName := ecsContainerNameLabel(docker, msg.ID) + ".service.discovery."
		switch msg.Status {
		case "start":
			if isObservedContainer(ecsContainerLabel) {
				glog.Infof("Event: container %s started. Creating record: %s", msg.ID, zoneName)
				exists, err := recordExists(client, *zoneId, zoneName, hostname(*metadataIP))
				if err != nil {
					glog.Errorf("Error checking for existing container: %v", err)
				}
				if !exists {
					weightedRequestForClientZone("CREATE", zoneName, hostname(*metadataIP))
					if err != nil {
						glog.Errorf("Error creating route")
					} else {
						glog.Info("Create attempt complete.")
					}
				} else {
					glog.Infof("Record already exists. Not creating")
				}
			}
		case "die", "stop", "kill": // FIXME. More than one of these can happen
		// FIXME: Think about one day how we might have multiple services running on a single host. Then we should switch to SRV records and take the port into account.
			if isObservedContainer(ecsContainerLabel) {
				glog.Infof("Event: container %s %s. Deleting Record: %s", msg.ID, *msg, zoneName)
				exists, err := recordExists(client, *zoneId, zoneName, hostname(*metadataIP))
				if err != nil {
					glog.Errorf("Error checking for existing container: %v", err)
				}
				if exists {
					weightedRequestForClientZone("DELETE", zoneName, hostname(*metadataIP))
					glog.Info("Deletion attempt complete.")
				} else {
					glog.Infof("Suitable record doesn't exist. Not deleting")
				}
			}
		case "default":
			glog.Infof("Event: container %s ignoring", msg.ID)
		}
	}

	quit := make(chan struct{})
	close(quit)
}
