// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package proxmox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Telmate/proxmox-api-go/proxmox"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

type finalizerMock struct {
	getConfig  func() (map[string]interface{}, error)
	setConfig  func(map[string]interface{}) (string, error)
	startVm    func() (string, error)
	shutdownVm func() (string, error)
}

func (m finalizerMock) GetVmConfig(*proxmox.VmRef) (map[string]interface{}, error) {
	return m.getConfig()
}
func (m finalizerMock) SetVmConfig(vmref *proxmox.VmRef, c map[string]interface{}) (interface{}, error) {
	return m.setConfig(c)
}

func (m finalizerMock) StartVm(*proxmox.VmRef) (string, error) {
	return m.startVm()
}

func (m finalizerMock) ShutdownVm(*proxmox.VmRef) (string, error) {
	return m.shutdownVm()
}

var _ finalizer = finalizerMock{}

func TestTemplateFinalize(t *testing.T) {
	cs := []struct {
		name                string
		builderConfig       *Config
		initialVMConfig     map[string]interface{}
		getConfigErr        error
		expectCallSetConfig bool
		expectedVMConfig    map[string]interface{}
		setConfigErr        error
		expectedAction      multistep.StepAction
		expectedDelete      []string
	}{
		{
			name:          "empty config changes name and description",
			builderConfig: &Config{},
			initialVMConfig: map[string]interface{}{
				"name":        "dummy",
				"description": "Packer ephemeral build VM",
				"ide2":        "local:iso/Fedora-Server-dvd-x86_64-29-1.2.iso,media=cdrom",
			},
			expectCallSetConfig: true,
			expectedVMConfig: map[string]interface{}{
				"name":        "",
				"description": "",
				"ide2":        nil,
			},
			expectedAction: multistep.ActionContinue,
		},
		{
			name: "use VM name when template name not provided",
			builderConfig: &Config{
				VMName: "my-vm",
			},
			initialVMConfig: map[string]interface{}{
				"name": "dummy",
			},
			expectCallSetConfig: true,
			expectedVMConfig: map[string]interface{}{
				"name": "my-vm",
			},
			expectedAction: multistep.ActionContinue,
		},
		{
			name: "use template name when both VM name and template name are provided",
			builderConfig: &Config{
				VMName:       "my-vm",
				TemplateName: "my-template",
			},
			initialVMConfig: map[string]interface{}{
				"name": "dummy",
			},
			expectCallSetConfig: true,
			expectedVMConfig: map[string]interface{}{
				"name": "my-template",
			},
			expectedAction: multistep.ActionContinue,
		},
		{
			name: "all options",
			builderConfig: &Config{
				TemplateName:        "my-template",
				TemplateDescription: "some-description",
			},
			initialVMConfig: map[string]interface{}{
				"name":        "dummy",
				"description": "Packer ephemeral build VM",
			},
			expectCallSetConfig: true,
			expectedVMConfig: map[string]interface{}{
				"name":        "my-template",
				"description": "some-description",
			},
			expectedAction: multistep.ActionContinue,
		},
		{
			name: "all options with cloud-init",
			builderConfig: &Config{
				TemplateName:        "my-template",
				TemplateDescription: "some-description",
				CloudInit:           true,
				CloudInitDiskType:   "ide",
			},
			initialVMConfig: map[string]interface{}{
				"name":        "dummy",
				"description": "Packer ephemeral build VM",
				"bootdisk":    "virtio0",
				"virtio0":     "ceph01:base-223-disk-0,cache=unsafe,media=disk,size=32G",
			},
			expectCallSetConfig: true,
			expectedVMConfig: map[string]interface{}{
				"name":        "my-template",
				"description": "some-description",
				"ide0":        "ceph01:cloudinit",
			},
			expectedAction: multistep.ActionContinue,
		},
		{
			name: "no available controller for cloud-init drive",
			builderConfig: &Config{
				TemplateName:        "my-template",
				TemplateDescription: "some-description",
				CloudInit:           true,
				CloudInitDiskType:   "ide",
			},
			initialVMConfig: map[string]interface{}{
				"name":        "dummy",
				"description": "Packer ephemeral build VM",
				"ide0":        "local:iso/Fedora-Server-dvd-x86_64-29-1.2.iso,media=cdrom",
				"ide1":        "local:iso/Fedora-Server-dvd-x86_64-29-1.2.iso,media=cdrom",
				"ide2":        "local:iso/Fedora-Server-dvd-x86_64-29-1.2.iso,media=cdrom",
				"ide3":        "local:iso/Fedora-Server-dvd-x86_64-29-1.2.iso,media=cdrom",
				"bootdisk":    "virtio0",
				"virtio0":     "ceph01:base-223-disk-0,cache=unsafe,media=disk,size=32G",
			},
			expectCallSetConfig: false,
			expectedAction:      multistep.ActionHalt,
		},
		{
			name: "GetVmConfig error should return halt",
			builderConfig: &Config{
				TemplateName:        "my-template",
				TemplateDescription: "some-description",
				CloudInit:           true,
				CloudInitDiskType:   "ide",
			},
			getConfigErr:        fmt.Errorf("some error"),
			expectCallSetConfig: false,
			expectedAction:      multistep.ActionHalt,
		},
		{
			name: "SetVmConfig error should return halt",
			builderConfig: &Config{
				TemplateName:        "my-template",
				TemplateDescription: "some-description",
			},
			initialVMConfig: map[string]interface{}{
				"name":        "dummy",
				"description": "Packer ephemeral build VM",
			},
			expectCallSetConfig: true,
			setConfigErr:        fmt.Errorf("some error"),
			expectedAction:      multistep.ActionHalt,
		},
		{
			name:          "find and remove unused disks",
			builderConfig: &Config{},
			initialVMConfig: map[string]interface{}{
				"unused0":  "local-zfs:vm-110-disk-1",
				"unused99": "local-zfs:vm-110-disk-100",
			},
			expectCallSetConfig: true,
			expectedDelete:      []string{"unused0", "unused99"},
			expectedAction:      multistep.ActionContinue,
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			finalizer := finalizerMock{
				getConfig: func() (map[string]interface{}, error) {
					return c.initialVMConfig, c.getConfigErr
				},
				setConfig: func(cfg map[string]interface{}) (string, error) {
					if !c.expectCallSetConfig {
						t.Error("Did not expect SetVmConfig to be called")
					}
					for key, val := range c.expectedVMConfig {
						if cfg[key] != val {
							t.Errorf("Expected %q to be %q, got %q", key, val, cfg[key])
						}
					}
					// We need to sort deletes first, to test them reliably
					var gotDelete = strings.Split(cfg["delete"].(string), ",")
					sort.Strings(gotDelete)
					sort.Strings(c.expectedDelete)
					if strings.Join(gotDelete, ",") != strings.Join(c.expectedDelete, ",") {
						t.Errorf("Expected delete to be %s, got %s", c.expectedDelete, cfg["delete"])
					}

					return "", c.setConfigErr
				},
			}

			state := new(multistep.BasicStateBag)
			state.Put("ui", packersdk.TestUi(t))
			state.Put("config", c.builderConfig)
			state.Put("vmRef", proxmox.NewVmRef(1))
			state.Put("proxmoxClient", finalizer)

			step := stepFinalizeConfig{}
			action := step.Run(context.TODO(), state)
			if action != c.expectedAction {
				t.Errorf("Expected action to be %v, got %v", c.expectedAction, action)
			}
		})
	}
}
