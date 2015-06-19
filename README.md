# Route53 Registrator

## What is it

Allows basic load balancedservice discovery using a Route53 private hosted zone in your VPC.

 * Run this in a Docker container on each AWS ECS instances in your cluster.
 * It listens to Docker events when services are stopped and started and adds and removes A records to a private hosted 
 zone.
 * It follows the convention of looking at the ECS related tags on containers. If the "family" specified in the task 
 definition ends with ".service" then the container is treated as something that should be registered in DNS.
 * The A record is registered under a zone using this naming convention <container-name>.service.discovery.
 * Route53 can only run health checks on internet accessible hosts so there is no health checking code.

Forked from [brandnetworks/route53-registrator](https://github.com/brandnetworks/route53-registrator).

Inspiration:
 * https://github.com/RichardBronosky/aws-ecs-service-discovery
 * http://blog.xi-group.com/2014/06/how-to-implement-service-discovery-in-the-cloud/


Inspired by [registrator](https://github.com/gliderlabs/registrator), route53-registrator watches for docker events created by a named container, and creates or deletes Route53 CNAME records pointing to the host's ~~public~~ private address.

## Limitations

The program uses the ec2 metadata service to retrieve the hostname for the instance. As a result, the program is tied to usage on AWS EC2 instances for now.

## Development

### Setup

- Ensure you have a working golang environment
- `go get` to install dependencies

### Make tasks:

 - `build/container`: 
     - Builds the binary image (compiled only for linux amd64)
     - Builds a Docker container to run the binary
 - `dev`:
     - Runs the latest built docker container, passing AWS credentials as env variables and 
       some sane defaults
 - `release`:
     - Pushes the latest image to the public docker index (it's tied to my account right now)


## A note on `ca-bundle.crt`:

This file contains a set of trusted root certificates obtained from Mozilla [here](http://hg.mozilla.org/releases/mozilla-release/raw-file/default/security/nss/lib/ckfw/builtins/certdata.txt)
