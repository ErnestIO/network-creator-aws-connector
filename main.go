/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"fmt"
	"log"
	"os"
	"runtime"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/nats-io/nats"
)

var nc *nats.Conn
var natsErr error

func eventHandler(m *nats.Msg) {
	var n Event

	err := n.Process(m.Data)
	if err != nil {
		nc.Publish("network.create.aws.error", m.Data)
		return
	}

	if err = n.Validate(); err != nil {
		n.Error(err)
		return
	}

	err = createNetwork(&n)
	if err != nil {
		n.Error(err)
		return
	}

	n.Complete()
}

func createNetwork(ev *Event) error {
	creds := credentials.NewStaticCredentials(ev.DatacenterAccessKey, ev.DatacenterAccessToken, "")
	svc := ec2.New(session.New(), &aws.Config{
		Region:      aws.String(ev.DatacenterRegion),
		Credentials: creds,
	})

	req := ec2.CreateSubnetInput{
		VpcId:     aws.String(ev.DatacenterVPCID),
		CidrBlock: aws.String(ev.NetworkSubnet),
	}

	resp, err := svc.CreateSubnet(&req)
	if err != nil {
		return err
	}

	ev.NetworkAWSID = *resp.Subnet.SubnetId

	return nil
}

func main() {
	natsURI := os.Getenv("NATS_URI")
	if natsURI == "" {
		natsURI = nats.DefaultURL
	}

	nc, natsErr = nats.Connect(natsURI)
	if natsErr != nil {
		log.Fatal(natsErr)
	}

	fmt.Println("listening for network.create.aws")
	nc.Subscribe("network.create.aws", eventHandler)

	runtime.Goexit()
}
