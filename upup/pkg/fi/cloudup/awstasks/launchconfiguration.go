package awstasks

import (
	"fmt"

	"encoding/base64"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/golang/glog"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/upup/pkg/fi/cloudup/terraform"
	"strings"
)

//go:generate fitask -type=LaunchConfiguration
type LaunchConfiguration struct {
	Name *string

	UserData *fi.ResourceHolder

	ImageID            *string
	InstanceType       *string
	SSHKey             *SSHKey
	SecurityGroups     []*SecurityGroup
	AssociatePublicIP  *bool
	IAMInstanceProfile *IAMInstanceProfile

	// RootVolumeSize is the size of the EBS root volume to use, in GB
	RootVolumeSize *int64
	// RootVolumeType is the type of the EBS root volume to use (e.g. gp2)
	RootVolumeType *string

	// SpotPrice is set to the spot-price bid if this is a spot pricing request
	SpotPrice *string

	ID *string
}

var _ fi.CompareWithID = &LaunchConfiguration{}

func (e *LaunchConfiguration) CompareWithID() *string {
	return e.ID
}

func (e *LaunchConfiguration) Find(c *fi.Context) (*LaunchConfiguration, error) {
	cloud := c.Cloud.(*awsup.AWSCloud)

	request := &autoscaling.DescribeLaunchConfigurationsInput{}

	prefix := *e.Name + "-"

	configurations := map[string]*autoscaling.LaunchConfiguration{}
	err := cloud.Autoscaling.DescribeLaunchConfigurationsPages(request, func(page *autoscaling.DescribeLaunchConfigurationsOutput, lastPage bool) bool {
		for _, l := range page.LaunchConfigurations {
			name := aws.StringValue(l.LaunchConfigurationName)
			if strings.HasPrefix(name, prefix) {
				suffix := name[len(prefix):]
				configurations[suffix] = l
			}
		}
		return true
	})

	if len(configurations) == 0 {
		return nil, nil
	}

	var newest *autoscaling.LaunchConfiguration
	var newestTime int64
	for _, lc := range configurations {
		t := lc.CreatedTime.UnixNano()
		if t > newestTime {
			newestTime = t
			newest = lc
		}
	}

	lc := newest

	glog.V(2).Infof("found existing AutoscalingLaunchConfiguration: %q", *lc.LaunchConfigurationName)

	actual := &LaunchConfiguration{
		Name:               e.Name,
		ID:                 lc.LaunchConfigurationName,
		ImageID:            lc.ImageId,
		InstanceType:       lc.InstanceType,
		SSHKey:             &SSHKey{Name: lc.KeyName},
		AssociatePublicIP:  lc.AssociatePublicIpAddress,
		IAMInstanceProfile: &IAMInstanceProfile{Name: lc.IamInstanceProfile},
		SpotPrice:          lc.SpotPrice,
	}

	securityGroups := []*SecurityGroup{}
	for _, sgID := range lc.SecurityGroups {
		securityGroups = append(securityGroups, &SecurityGroup{ID: sgID})
	}
	actual.SecurityGroups = securityGroups

	// Find the root volume
	for _, b := range lc.BlockDeviceMappings {
		if b.Ebs == nil || b.Ebs.SnapshotId != nil {
			// Not the root
			continue
		}
		actual.RootVolumeSize = b.Ebs.VolumeSize
		actual.RootVolumeType = b.Ebs.VolumeType
	}

	userData, err := base64.StdEncoding.DecodeString(*lc.UserData)
	if err != nil {
		return nil, fmt.Errorf("error decoding UserData: %v", err)
	}
	actual.UserData = fi.WrapResource(fi.NewStringResource(string(userData)))

	// Avoid spurious changes on ImageId
	if e.ImageID != nil && actual.ImageID != nil && *actual.ImageID != *e.ImageID {
		image, err := cloud.ResolveImage(*e.ImageID)
		if err != nil {
			glog.Warningf("unable to resolve image: %q: %v", *e.ImageID, err)
		} else if image == nil {
			glog.Warningf("unable to resolve image: %q: not found", *e.ImageID)
		} else if aws.StringValue(image.ImageId) == *actual.ImageID {
			glog.V(4).Infof("Returning matching ImageId as expected name: %q -> %q", *actual.ImageID, *e.ImageID)
			actual.ImageID = e.ImageID
		}
	}

	if e.ID == nil {
		e.ID = actual.ID
	}

	return actual, nil
}

func buildEphemeralDevices(instanceTypeName *string) (map[string]*BlockDeviceMapping, error) {
	// TODO: Any reason not to always attach the ephemeral devices?
	if instanceTypeName == nil {
		return nil, fi.RequiredField("InstanceType")
	}
	instanceType, err := awsup.GetMachineTypeInfo(*instanceTypeName)
	if err != nil {
		return nil, err
	}
	blockDeviceMappings := make(map[string]*BlockDeviceMapping)
	for _, ed := range instanceType.EphemeralDevices() {
		m := &BlockDeviceMapping{VirtualName: fi.String(ed.VirtualName)}
		blockDeviceMappings[ed.DeviceName] = m
	}
	return blockDeviceMappings, nil
}

func (e *LaunchConfiguration) buildRootDevice(cloud *awsup.AWSCloud) (map[string]*BlockDeviceMapping, error) {
	imageID := fi.StringValue(e.ImageID)
	image, err := cloud.ResolveImage(imageID)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve image: %q: %v", imageID, err)
	} else if image == nil {
		return nil, fmt.Errorf("unable to resolve image: %q: not found", imageID)
	}

	rootDeviceName := aws.StringValue(image.RootDeviceName)

	blockDeviceMappings := make(map[string]*BlockDeviceMapping)

	rootDeviceMapping := &BlockDeviceMapping{
		EbsDeleteOnTermination: aws.Bool(true),
		EbsVolumeSize:          e.RootVolumeSize,
		EbsVolumeType:          e.RootVolumeType,
	}

	blockDeviceMappings[rootDeviceName] = rootDeviceMapping

	return blockDeviceMappings, nil
}

func (e *LaunchConfiguration) Run(c *fi.Context) error {
	return fi.DefaultDeltaRunMethod(e, c)
}

func (s *LaunchConfiguration) CheckChanges(a, e, changes *LaunchConfiguration) error {
	if e.ImageID == nil {
		return fi.RequiredField("ImageID")
	}
	if e.InstanceType == nil {
		return fi.RequiredField("InstanceType")
	}

	if a != nil {
		if e.Name == nil {
			return fi.RequiredField("Name")
		}
	}
	return nil
}

func (_ *LaunchConfiguration) RenderAWS(t *awsup.AWSAPITarget, a, e, changes *LaunchConfiguration) error {
	launchConfigurationName := *e.Name + "-" + fi.BuildTimestampString()
	glog.V(2).Infof("Creating AutoscalingLaunchConfiguration with Name:%q", launchConfigurationName)

	if e.ImageID == nil {
		return fi.RequiredField("ImageID")
	}
	image, err := t.Cloud.ResolveImage(*e.ImageID)
	if err != nil {
		return err
	}

	request := &autoscaling.CreateLaunchConfigurationInput{}
	request.LaunchConfigurationName = &launchConfigurationName
	request.ImageId = image.ImageId
	request.InstanceType = e.InstanceType
	if e.SSHKey != nil {
		request.KeyName = e.SSHKey.Name
	}
	securityGroupIDs := []*string{}
	for _, sg := range e.SecurityGroups {
		securityGroupIDs = append(securityGroupIDs, sg.ID)
	}
	request.SecurityGroups = securityGroupIDs
	request.AssociatePublicIpAddress = e.AssociatePublicIP
	request.SpotPrice = e.SpotPrice

	// Build up the actual block device mappings
	{
		rootDevices, err := e.buildRootDevice(t.Cloud)
		if err != nil {
			return err
		}

		ephemeralDevices, err := buildEphemeralDevices(e.InstanceType)
		if err != nil {
			return err
		}

		if len(rootDevices) != 0 || len(ephemeralDevices) != 0 {
			request.BlockDeviceMappings = []*autoscaling.BlockDeviceMapping{}
			for device, bdm := range rootDevices {
				request.BlockDeviceMappings = append(request.BlockDeviceMappings, bdm.ToAutoscaling(device))
			}
			for device, bdm := range ephemeralDevices {
				request.BlockDeviceMappings = append(request.BlockDeviceMappings, bdm.ToAutoscaling(device))
			}
		}
	}

	if e.UserData != nil {
		d, err := e.UserData.AsBytes()
		if err != nil {
			return fmt.Errorf("error rendering AutoScalingLaunchConfiguration UserData: %v", err)
		}
		request.UserData = aws.String(base64.StdEncoding.EncodeToString(d))
	}
	if e.IAMInstanceProfile != nil {
		request.IamInstanceProfile = e.IAMInstanceProfile.Name
	}

	_, err = t.Cloud.Autoscaling.CreateLaunchConfiguration(request)

	if err != nil {
		if awsup.AWSErrorCode(err) == "ValidationError" {
			message := awsup.AWSErrorMessage(err)
			if strings.Contains(message, "not authorized") || strings.Contains(message, "Invalid IamInstance") {
				// IAM instance profile creation is notoriously async
				return fmt.Errorf("IAM instance profile not yet created/propagated")
			}
		}
		glog.V(4).Infof("ErrorCode=%q, Message=%q", awsup.AWSErrorCode(err), awsup.AWSErrorMessage(err))
		return fmt.Errorf("error creating AutoscalingLaunchConfiguration: %v", err)
	}

	e.ID = fi.String(launchConfigurationName)

	return nil // No tags on a launch configuration
}

type terraformLaunchConfiguration struct {
	NamePrefix               *string                 `json:"name_prefix,omitempty"`
	ImageID                  *string                 `json:"image_id,omitempty"`
	InstanceType             *string                 `json:"instance_type,omitempty"`
	KeyName                  *terraform.Literal      `json:"key_name,omitempty"`
	IAMInstanceProfile       *terraform.Literal      `json:"iam_instance_profile,omitempty"`
	SecurityGroups           []*terraform.Literal    `json:"security_groups,omitempty"`
	AssociatePublicIpAddress *bool                   `json:"associate_public_ip_address,omitempty"`
	UserData                 *terraform.Literal      `json:"user_data,omitempty"`
	RootBlockDevice          *terraformBlockDevice   `json:"root_block_device,omitempty"`
	EphemeralBlockDevice     []*terraformBlockDevice `json:"ephemeral_block_device,omitempty"`
	Lifecycle                *terraformLifecycle     `json:"lifecycle,omitempty"`
	SpotPrice                *string                 `json:"spot_price,omitempty"`
}

type terraformBlockDevice struct {
	// For ephemeral devices
	DeviceName  *string `json:"device_name,omitempty"`
	VirtualName *string `json:"virtual_name,omitempty"`

	// For root
	VolumeType          *string `json:"volume_type,omitempty"`
	VolumeSize          *int64  `json:"volume_size,omitempty"`
	DeleteOnTermination *bool   `json:"delete_on_termination,omitempty"`
}

type terraformLifecycle struct {
	CreateBeforeDestroy *bool `json:"create_before_destroy,omitempty"`
}

func (_ *LaunchConfiguration) RenderTerraform(t *terraform.TerraformTarget, a, e, changes *LaunchConfiguration) error {
	cloud := t.Cloud.(*awsup.AWSCloud)

	if e.ImageID == nil {
		return fi.RequiredField("ImageID")
	}
	image, err := cloud.ResolveImage(*e.ImageID)
	if err != nil {
		return err
	}

	tf := &terraformLaunchConfiguration{
		NamePrefix:   fi.String(*e.Name + "-"),
		ImageID:      image.ImageId,
		InstanceType: e.InstanceType,
		SpotPrice:    e.SpotPrice,
	}

	if e.SSHKey != nil {
		tf.KeyName = e.SSHKey.TerraformLink()
	}

	for _, sg := range e.SecurityGroups {
		tf.SecurityGroups = append(tf.SecurityGroups, sg.TerraformLink())
	}
	tf.AssociatePublicIpAddress = e.AssociatePublicIP

	{
		rootDevices, err := e.buildRootDevice(cloud)
		if err != nil {
			return err
		}

		ephemeralDevices, err := buildEphemeralDevices(e.InstanceType)
		if err != nil {
			return err
		}

		if len(rootDevices) != 0 {
			if len(rootDevices) != 1 {
				return fmt.Errorf("unexpectedly found multiple root devices")
			}

			for _, bdm := range rootDevices {
				tf.RootBlockDevice = &terraformBlockDevice{
					VolumeType:          bdm.EbsVolumeType,
					VolumeSize:          bdm.EbsVolumeSize,
					DeleteOnTermination: fi.Bool(true),
				}
			}
		}

		if len(ephemeralDevices) != 0 {
			tf.EphemeralBlockDevice = []*terraformBlockDevice{}
			for deviceName, bdm := range ephemeralDevices {
				tf.EphemeralBlockDevice = append(tf.EphemeralBlockDevice, &terraformBlockDevice{
					VirtualName: bdm.VirtualName,
					DeviceName:  fi.String(deviceName),
				})
			}
		}
	}

	if e.UserData != nil {
		tf.UserData, err = t.AddFile("aws_launch_configuration", *e.Name, "user_data", e.UserData)
		if err != nil {
			return err
		}
	}
	if e.IAMInstanceProfile != nil {
		tf.IAMInstanceProfile = e.IAMInstanceProfile.TerraformLink()
	}

	// So that we can update configurations
	tf.Lifecycle = &terraformLifecycle{CreateBeforeDestroy: fi.Bool(true)}

	return t.RenderResource("aws_launch_configuration", *e.Name, tf)
}

func (e *LaunchConfiguration) TerraformLink() *terraform.Literal {
	return terraform.LiteralProperty("aws_launch_configuration", *e.Name, "id")
}
