/*
 * Copyright (c) 2019 Huawei Technologies Co., Ltd.
 * MeshAccelerating is licensed under the Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *     http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND, EITHER EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT, MERCHANTABILITY OR FIT FOR A PARTICULAR
 * PURPOSE.
 * See the Mulan PSL v2 for more details.
 * Author: LemmyHuang
 * Create: 2021-10-09
 */

package option

// #cgo CFLAGS: -I../../bpf/include
// #include "config.h"
import "C"
import (
	"fmt"
)

const (
	ClientModeKube = "kubernetes"
	ClientModeEnvoy = "envoy"
)

var (
	config	DaemonConfig
)

type BpfConfig struct {
	BpffsPath	string
	Cgroup2Path	string
}
type ClientConfig struct {
	ClientMode		string
	KubeInCluster	bool
	EnableL7Policy	bool
}

type DaemonConfig struct {
	BpfConfig
	ClientConfig
}

func InitializeDaemonConfig() error {
	dc := &config

	dc.BpfConfig.BpffsPath = "/sys/fs/bpf/"
	dc.BpfConfig.Cgroup2Path = "/mnt/cgroup2/"

	dc.ClientConfig.ClientMode = ClientModeKube
	dc.ClientConfig.KubeInCluster = false
	dc.ClientConfig.EnableL7Policy = C.KMESH_ENABLE_HTTP == C.KMESH_MODULE_ON

	fmt.Println(config)
	return nil
}

func (dc *DaemonConfig) String() string {
	return fmt.Sprintf("%#v", *dc)
}

func GetBpfConfig() BpfConfig {
	return config.BpfConfig
}

func GetClientConfig() ClientConfig {
	return config.ClientConfig
}