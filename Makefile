NAME=spaaza/route53-registrator

dev:
	docker run \
		-e DOCKER_HOST=http://192.168.59.103:2375 \
		-e AWS_ACCESS_KEY_ID=$(AWS_ACCESS_KEY_ID) \
		-e AWS_SECRET_ACCESS_KEY=$(AWS_SECRET_ACCESS_KEY) \
		$(NAME) /bin/route53-registrator -metadata=192.168.59.103 -zone=Z1P7DHMHEAX6O3 -logtostderr=1


image:
	docker run --rm -v $(shell pwd):/src -v /var/run/docker.sock:/var/run/docker.sock centurylink/golang-builder spaaza/route53-registrator

release:
	docker push spaaza/route53-registrator
