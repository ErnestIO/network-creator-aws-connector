/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	ecc "github.com/ernestio/ernest-config-client"
	"github.com/nats-io/nats"
)

var nc *nats.Conn
var natsErr error

func eventHandler(m *nats.Msg) {
	var n Event

	err := n.Process(m.Data)
	if err != nil {
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

func internetGatewayByVPCID(svc *ec2.EC2, vpc string) (*ec2.InternetGateway, error) {
	f := []*ec2.Filter{
		&ec2.Filter{
			Name:   aws.String("attachment.vpc-id"),
			Values: []*string{aws.String(vpc)},
		},
	}

	req := ec2.DescribeInternetGatewaysInput{
		Filters: f,
	}

	resp, err := svc.DescribeInternetGateways(&req)
	if err != nil {
		return nil, err
	}

	if len(resp.InternetGateways) == 0 {
		return nil, nil
	}

	return resp.InternetGateways[0], nil
}

func routingTableBySubnetID(svc *ec2.EC2, subnet string) (*ec2.RouteTable, error) {
	f := []*ec2.Filter{
		&ec2.Filter{
			Name:   aws.String("association.subnet-id"),
			Values: []*string{aws.String(subnet)},
		},
	}

	req := ec2.DescribeRouteTablesInput{
		Filters: f,
	}

	resp, err := svc.DescribeRouteTables(&req)
	if err != nil {
		return nil, err
	}

	if len(resp.RouteTables) == 0 {
		return nil, nil
	}

	return resp.RouteTables[0], nil
}

func createInternetGateway(svc *ec2.EC2, vpc string) (*ec2.InternetGateway, error) {
	ig, err := internetGatewayByVPCID(svc, vpc)
	if err != nil {
		return nil, err
	}

	if ig != nil {
		return ig, nil
	}

	resp, err := svc.CreateInternetGateway(nil)
	if err != nil {
		return nil, err
	}

	req := ec2.AttachInternetGatewayInput{
		InternetGatewayId: resp.InternetGateway.InternetGatewayId,
		VpcId:             aws.String(vpc),
	}

	_, err = svc.AttachInternetGateway(&req)
	if err != nil {
		return nil, err
	}

	return resp.InternetGateway, nil
}

func createRouteTable(svc *ec2.EC2, vpc, subnet string) (*ec2.RouteTable, error) {
	rt, err := routingTableBySubnetID(svc, subnet)
	if err != nil {
		return nil, err
	}

	if rt != nil {
		return rt, nil
	}

	req := ec2.CreateRouteTableInput{
		VpcId: aws.String(vpc),
	}

	resp, err := svc.CreateRouteTable(&req)
	if err != nil {
		return nil, err
	}

	acreq := ec2.AssociateRouteTableInput{
		RouteTableId: resp.RouteTable.RouteTableId,
		SubnetId:     aws.String(subnet),
	}

	_, err = svc.AssociateRouteTable(&acreq)
	if err != nil {
		return nil, err
	}

	return resp.RouteTable, nil
}

func createGatewayRoutes(svc *ec2.EC2, rt *ec2.RouteTable, gw *ec2.InternetGateway) error {
	req := ec2.CreateRouteInput{
		RouteTableId:         rt.RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            gw.InternetGatewayId,
	}

	_, err := svc.CreateRoute(&req)
	if err != nil {
		return err
	}

	return nil
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

	if ev.NetworkIsPublic {
		// Create Internet Gateway
		gateway, err := createInternetGateway(svc, ev.DatacenterVPCID)
		if err != nil {
			return err
		}

		// Create Route Table and direct traffic to Internet Gateway
		rt, err := createRouteTable(svc, ev.DatacenterVPCID, *resp.Subnet.SubnetId)
		if err != nil {
			return err
		}

		err = createGatewayRoutes(svc, rt, gateway)
		if err != nil {
			return err
		}

		// Modify subnet to assign public IP's on launch
		mod := ec2.ModifySubnetAttributeInput{
			SubnetId:            resp.Subnet.SubnetId,
			MapPublicIpOnLaunch: &ec2.AttributeBooleanValue{Value: aws.Bool(true)},
		}

		_, err = svc.ModifySubnetAttribute(&mod)
		if err != nil {
			return err
		}
	}

	ev.NetworkAWSID = *resp.Subnet.SubnetId

	return nil
}

func main() {
	nc = ecc.NewConfig(os.Getenv("NATS_URI")).Nats()

	fmt.Println("listening for network.create.aws")
	nc.Subscribe("network.create.aws", eventHandler)

	runtime.Goexit()
}
