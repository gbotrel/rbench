package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

var (
	awsConfig   aws.Config
	ec2Client   *ec2.Client
	awsUserName string
	awsKeyName  string
)

func initAWS() error {
	var err error
	awsConfig, err = config.LoadDefaultConfig(context.TODO(), config.WithRegion("us-east-2"))
	if err != nil {
		return fmt.Errorf("unable to load SDK config, %v", err)
	}

	ec2Client = ec2.NewFromConfig(awsConfig)

	// Create an IAM service client
	svc := iam.NewFromConfig(awsConfig)

	// Get the IAM user information
	iamResult, err := svc.GetUser(context.TODO(), &iam.GetUserInput{})
	if err != nil {
		return fmt.Errorf("unable to get user, %v", err)
	}
	awsUserName = *iamResult.User.UserName

	// create key pair
	awsKeyName = "rbench-" + awsUserName

	// Create the key pair
	result, err := ec2Client.CreateKeyPair(context.TODO(), &ec2.CreateKeyPairInput{
		KeyName: aws.String(awsKeyName),
	})

	if err == nil {
		// Save the private key material to a file
		// privateKeyPath is home directory + .ssh
		err = os.WriteFile(privateKeyPath(), []byte(*result.KeyMaterial), 0600)
		if err != nil {
			return fmt.Errorf("unable to write private key to file, %v", err)
		}
	}
	if err != nil && !strings.Contains(err.Error(), "InvalidKeyPair.Duplicate") {
		return err
	}

	return nil
}

type instanceArch uint8

const (
	archUnknown instanceArch = iota
	archArm
	archX86
)

func (a instanceArch) GoString() string {
	switch a {
	case archArm:
		return "arm64"
	case archX86:
		return "amd64"
	default:
		return "unknown"
	}
}

func getInstanceArch() (arch instanceArch, err error) {
	// Call DescribeInstanceTypes API
	describeInstanceTypesInput := &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{
			types.InstanceType(*instanceType),
		},
	}

	describeInstanceTypesOutput, err := ec2Client.DescribeInstanceTypes(context.TODO(), describeInstanceTypesInput)
	if err != nil {
		return archUnknown, fmt.Errorf("unable to describe instance types, %v", err)
	}

	// we want to know if it's arm or x86
	architecture := describeInstanceTypesOutput.InstanceTypes[0].ProcessorInfo.SupportedArchitectures[0]

	if architecture == types.ArchitectureTypeArm64 {
		return archArm, nil
	}

	return archX86, nil
}

func startInstance(arch instanceArch) (publicIP, instanceID string, err error) {

	// 	Ubuntu Server 24.04 LTS (HVM), SSD Volume Type
	// ami-0ea3c35c5c3284d82 (64-bit (x86)) / ami-01ebf7c0e446f85f9 (64-bit (Arm))

	const (
		x86AMI = "ami-0ea3c35c5c3284d82"
		armAMI = "ami-01ebf7c0e446f85f9"
	)

	var ami string
	if arch == archArm {
		ami = armAMI
	} else {
		ami = x86AMI
	}

	// Define the parameters for the EC2 instance
	instanceName := fmt.Sprintf("rbench/%s/%s", awsUserName, randString(7))

	runResult, err := ec2Client.RunInstances(context.TODO(), &ec2.RunInstancesInput{
		ImageId:      aws.String(ami), // Ubuntu Server 24.04 LTS
		InstanceType: types.InstanceType(*instanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      aws.String(awsKeyName),
		SecurityGroupIds: []string{
			"sg-02718b1d52ed88934", // default security group
		},

		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("rbench"),
						Value: aws.String(awsUserName),
					},
					{
						Key:   aws.String("Name"),
						Value: aws.String(instanceName),
					},
				},
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("unable to run instance, %v", err)
	}
	if len(runResult.Instances) != 1 {
		return "", "", fmt.Errorf("expected 1 instance, got %d", len(runResult.Instances))
	}
	instanceID = *runResult.Instances[0].InstanceId

	// wait for the instance to be running
	waiter := ec2.NewInstanceRunningWaiter(ec2Client)
	describeResult, err := waiter.WaitForOutput(context.TODO(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 2*time.Minute)

	if err != nil {
		terminateInstance(instanceID)
		return "", "", fmt.Errorf("error waiting for instance to be running, %v", err)
	}

	publicIP = *describeResult.Reservations[0].Instances[0].PublicIpAddress

	// Check if SSH port is accessible
	timeout := 30 * time.Second
	for i := 0; i < 5; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", publicIP), timeout)
		if err == nil {
			conn.Close()
			time.Sleep(5 * time.Second)
			return publicIP, instanceID, nil
		}
		time.Sleep(5 * time.Second)
	}

	terminateInstance(instanceID)
	return "", "", fmt.Errorf("unable to connect to instance")
}

func terminateInstance(instanceID string) error {
	fmt.Printf("terminating instance %s\n", instanceID)
	_, err := ec2Client.TerminateInstances(context.TODO(), &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		err = fmt.Errorf("unable to terminate instance, %v", err)
		fmt.Println("error: ", err)
		return err
	}
	return nil
}

func privateKeyPath() string {
	return os.Getenv("HOME") + "/.ssh/" + awsKeyName + ".pem"
}
