package main

import (
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/awslabs/aws-sdk-go/aws"
	"github.com/awslabs/aws-sdk-go/aws/awsutil"
	"github.com/awslabs/aws-sdk-go/service/route53"
	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/golang/glog"
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

//uses the ec2 metadata service to retrieve the public
//cname for the instance
func hostname(metadataServerAddress string) (hostname string) {
	host := []string{"http:/", metadataServerAddress, "latest", "meta-data", "public-hostname"}
	resp, err := http.Get(strings.Join(host, "/"))
	assert(err)

	defer resp.Body.Close()
	assert(err)
	body, err := ioutil.ReadAll(resp.Body)
	assert(err)
	return string(body)
}

//container names start with a /. This function removes the leading / if it exists.
func normalizedContainerName(original string) (normalized string) {
	if strings.HasPrefix("/", original) {
		return original
	}
	return strings.Join([]string{"/", original}, "")
}

//Given a container ID and a name, assert whether the name of the container matches that of the provided name.
func isObservedContainer(client *dockerapi.Client, containerId string, targetContainerName string) (observed bool) {
	container, err := client.InspectContainer(containerId)
	assert(err)
	if container.Name == targetContainerName {
		return true
	}
	glog.Infof("Container ", containerId, " did not match name", targetContainerName)
	return false
}

//Find all resource records in a AWS Hosted Zone that match a given name.
func findMatchingResourceRecords(client *route53.Route53, zone string, setName string) (recordSet []*route53.ResourceRecordSet, err error) {
	resources, err := client.ListResourceRecordSets(&route53.ListResourceRecordSetsInput{
		HostedZoneID: aws.String(zone),
	})
	if awserr := aws.Error(err); awserr != nil {
		// A service error occurred.
		glog.Errorf("AWS Error: \n Code: %s \n Message: %s", awserr.Code, awserr.Message)
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
			glog.Infof("Found existing ResourceRecord Set with name ", strings.TrimRight(setName, "."))
			matching = append(matching, route)
		}
	}
	return matching, nil
}

//Given a CNAME and a value, create a AWS Resource Record Set with CNAME -> Value.
func createResourceRecordSet(cname string, value string) (resourceRecordSet *route53.ResourceRecordSet) {
	return &route53.ResourceRecordSet{
		Name: aws.String(cname),
		Type: aws.String("CNAME"),
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

func resourceRecordSetRequest(client *route53.Route53, action string, zoneId string, cname string, value string) (resp *route53.ChangeResourceRecordSetsOutput, err error) {
	changes := []*route53.Change{&route53.Change{
		Action:            aws.String(action),
		ResourceRecordSet: createResourceRecordSet(cname, value),
	}}
	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: changes,
		},
		HostedZoneID: aws.String(zoneId),
	}
	return client.ChangeResourceRecordSets(params)
}

func main() {
	var containerName = flag.String("container", "docker-registry", "The container to watch")
	var metadataIP = flag.String("metadata", "169.254.169.254", "The address of the metadata service")
	var region = flag.String("region", "us-east-1", "The region for route53 records")
	var zoneId = flag.String("zone", "Z1P7DHMHEAX6O3", "The route53 hosted zone id")
	var cname = flag.String("cname", "my-test-registry.realtime.bnservers.com", "The CNAME for the record set")

	//Print some debug information
	flag.Parse()
	glog.Info(*region)
	glog.Info(*metadataIP)
	glog.Info(*cname)
	glog.Info(*zoneId)
	glog.Info(*containerName)

	docker, err := dockerapi.NewClient(getopt("DOCKER_HOST", "unix:///tmp/docker.sock"))
	assert(err)
	err = docker.Ping()
	assert(err)

	//the container name we're looking for
	targetContainer := normalizedContainerName(*containerName)

	events := make(chan *dockerapi.APIEvents)
	assert(docker.AddEventListener(events))

	client := route53.New(nil)

	matchingResourceRecords, err := findMatchingResourceRecords(client, *zoneId, *cname)
	if err != nil {
		glog.Errorf("Error searching for exisiting records:", err)
	}
	glog.Infof("Found %d existing records with a matching name. Destroying. \n", len(matchingResourceRecords))
	if matchingResourceRecords != nil {
		var changes []*route53.Change
		for _, set := range matchingResourceRecords {
			changes = append(changes, &route53.Change{
				Action:            aws.String("DELETE"),
				ResourceRecordSet: set,
			})
		}
		params := &route53.ChangeResourceRecordSetsInput{
			ChangeBatch: &route53.ChangeBatch{
				Changes: changes,
			},
			HostedZoneID: aws.String(*zoneId),
		}
		resp, err := client.ChangeResourceRecordSets(params)
		if awserr := aws.Error(err); awserr != nil {
			glog.Errorf("AWS Error: \n Code: %s \n Message: %s", awserr.Code, awserr.Message)
		} else if err != nil {
			glog.Errorf("Error removing existing records: ", err)
			panic(err)
		}
		glog.Infof("Response from removing existing records: \n %+v", awsutil.StringValue(resp))
	}

	glog.Infof("Listening for Docker events ...")

	// Process Docker events
	for msg := range events {
		switch msg.Status {
		case "start":
			glog.Infof("Event: container %s died", msg.ID)
			if isObservedContainer(docker, msg.ID, targetContainer) {
				resp, err := resourceRecordSetRequest(client, "CREATE", *zoneId, *cname, hostname(*metadataIP))
				if awserr := aws.Error(err); awserr != nil {
					glog.Errorf("AWS Error: \n Code: %s \n Message: %s", awserr.Code, awserr.Message)
				} else if err != nil {
					// A non-service error occurred.
					glog.Errorf("Error occured creating records: \n %s", err)
					panic(err)
				}
				glog.Infof("Response: \n: %s", awsutil.StringValue(resp))
			}
		case "die":
			glog.Infof("Event: container %s died.", msg.ID)
			if isObservedContainer(docker, msg.ID, targetContainer) {
				resp, err := resourceRecordSetRequest(client, "DELETE", *zoneId, *cname, hostname(*metadataIP))
				if awserr := aws.Error(err); awserr != nil {
					glog.Errorf("AWS Error: \n Code: %s \n Message: %s", awserr.Code, awserr.Message)
				} else if err != nil {
					// A non-service error occurred.
					glog.Errorf("Error occured creating records: \n %s", err)
					panic(err)
				}
				glog.Infof("Response: \n: %s", awsutil.StringValue(resp))
			}
		case "default":
			glog.Infof("Event: container %s ignoring", msg.ID)
		}
	}

	quit := make(chan struct{})
	close(quit)
}
