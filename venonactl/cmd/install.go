package cmd

/*
Copyright 2019 The Codefresh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/codefresh-io/venona/venonactl/pkg/store"

	"github.com/codefresh-io/venona/venonactl/pkg/plugins"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	clusterNameMaxLength = 20
	namespaceMaxLength = 20
)

var installCmdOptions struct {
	dryRun                 bool
	clusterNameInCodefresh string
	kube                   struct {
		namespace    string
		inCluster    bool
		context      string
		nodeSelector string
	}
	storageClass string
	venona       struct {
		version string
	}
	setDefaultRuntime             bool
	installOnlyRuntimeEnvironment bool
	skipRuntimeInstallation       bool
	runtimeEnvironmentName        string
	kubernetesRunnerType          bool
	buildNodeSelector             string
	buildAnnotations              []string
}

// installCmd represents the install command
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Codefresh's runtime-environment",
	Run: func(cmd *cobra.Command, args []string) {
		s := store.GetStore()
		lgr := createLogger("Install", verbose)
		buildBasicStore(lgr)
		extendStoreWithCodefershClient(lgr)
		extendStoreWithKubeClient(lgr)

		builder := plugins.NewBuilder(lgr)
		isDefault := isUsingDefaultStorageClass(installCmdOptions.storageClass)

		builderInstallOpt := &plugins.InstallOptions{
			CodefreshHost:         s.CodefreshAPI.Host,
			CodefreshToken:        s.CodefreshAPI.Token,
			MarkAsDefault:         installCmdOptions.setDefaultRuntime,
			StorageClass:          installCmdOptions.storageClass,
			IsDefaultStorageClass: isDefault,
			DryRun:                installCmdOptions.dryRun,
			KubernetesRunnerType:  installCmdOptions.kubernetesRunnerType,
		}

		if installCmdOptions.kubernetesRunnerType {
			builder.Add(plugins.EnginePluginType)
		}

		if isDefault {
			builderInstallOpt.StorageClass = plugins.DefaultStorageClassNamePrefix
		}

		if installCmdOptions.kube.context == "" {
			config := clientcmd.GetConfigFromFileOrDie(s.KubernetesAPI.ConfigPath)
			installCmdOptions.kube.context = config.CurrentContext
			lgr.Debug("Kube Context is not set, using current context", "Kube-Context-Name", installCmdOptions.kube.context)
		}
		if installCmdOptions.kube.namespace == "" {
			installCmdOptions.kube.namespace = "default"
		}

		s.KubernetesAPI.InCluster = installCmdOptions.kube.inCluster
		s.KubernetesAPI.ContextName = installCmdOptions.kube.context
		s.KubernetesAPI.Namespace = installCmdOptions.kube.namespace

		kns, err := parseNodeSelector(installCmdOptions.kube.nodeSelector)
		if err != nil {
			dieOnError(err)
		}
		s.KubernetesAPI.NodeSelector = kns.String()

		if installCmdOptions.dryRun {
			s.DryRun = installCmdOptions.dryRun
			lgr.Info("Running in dry-run mode")
		}
		if installCmdOptions.venona.version != "" {
			version := installCmdOptions.venona.version
			lgr.Info("Version set manually", "version", version)
			s.Image.Tag = version
			s.Version.Latest.Version = version
		}
		s.ClusterInCodefresh = installCmdOptions.clusterNameInCodefresh
		if installCmdOptions.installOnlyRuntimeEnvironment == true && installCmdOptions.skipRuntimeInstallation == true {
			dieOnError(fmt.Errorf("Cannot use both flags skip-runtime-installation and only-runtime-environment"))
		}
		if installCmdOptions.installOnlyRuntimeEnvironment == true {
			builder.Add(plugins.RuntimeEnvironmentPluginType)
		} else if installCmdOptions.skipRuntimeInstallation == true {
			if installCmdOptions.runtimeEnvironmentName == "" {
				dieOnError(fmt.Errorf("runtime-environment flag is required when using flag skip-runtime-installation"))
			}
			s.RuntimeEnvironment = installCmdOptions.runtimeEnvironmentName
			lgr.Info("Skipping installation of runtime environment, installing venona only")
			builder.Add(plugins.VenonaPluginType)
		} else {
			builder.
				Add(plugins.RuntimeEnvironmentPluginType).
				Add(plugins.VenonaPluginType)
		}
		if isDefault {
			builder.Add(plugins.VolumeProvisionerPluginType)
		} else {
			lgr.Info("Custom StorageClass is set, skipping installation of default volume provisioner")
		}

		builderInstallOpt.ClusterName = s.KubernetesAPI.ContextName
		builderInstallOpt.RegisterWithAgent = true
		if s.ClusterInCodefresh != "" {
			builderInstallOpt.ClusterName = s.ClusterInCodefresh
			builderInstallOpt.RegisterWithAgent = false
		}
		builderInstallOpt.KubeBuilder = getKubeClientBuilder(builderInstallOpt.ClusterName, s.KubernetesAPI.Namespace, s.KubernetesAPI.ConfigPath, s.KubernetesAPI.InCluster)
		builderInstallOpt.ClusterNamespace = s.KubernetesAPI.Namespace

		annotations := make(map[string]string)
		for _, annotation := range installCmdOptions.buildAnnotations {
			v := strings.Split(annotation, "=")
			if len(v) != 2 {
				dieOnError(errors.New("annotations must be in form \"key=value\""))
			}
			annotations[v[0]] = v[1]
		}

		builderInstallOpt.Annotations = annotations

		bns, err := parseNodeSelector(installCmdOptions.buildNodeSelector)
		if err != nil {
			dieOnError(err)
		}
		s.CodefreshAPI.BuildNodeSelector = bns
		builderInstallOpt.BuildNodeSelector = bns

		err = validateInstallOptions(builderInstallOpt)
		if err != nil {
			dieOnError(err)
		}

		values := s.BuildValues()
		for _, p := range builder.Get() {
			values, err = p.Install(builderInstallOpt, values)
			if err != nil {
				dieOnError(err)
			}
		}
		lgr.Info("Installation completed Successfully")
	},
}

func init() {
	rootCmd.AddCommand(installCmd)

	viper.BindEnv("kube-namespace", "KUBE_NAMESPACE")
	viper.BindEnv("kube-context", "KUBE_CONTEXT")

	installCmd.Flags().StringVar(&installCmdOptions.clusterNameInCodefresh, "cluster-name", "", "cluster name (if not passed runtime-environment will be created cluster-less); this is a friendly name used for metadata does not need to match the literal cluster name.  Limited to 20 Characters.")
	installCmd.Flags().StringVar(&installCmdOptions.venona.version, "venona-version", "", "Version of venona to install (default is the latest)")
	installCmd.Flags().StringVar(&installCmdOptions.runtimeEnvironmentName, "runtime-environment", "", "if --skip-runtime-installation set, will try to configure venona on current runtime-environment")
	installCmd.Flags().StringVar(&installCmdOptions.kube.namespace, "kube-namespace", viper.GetString("kube-namespace"), "Name of the namespace on which venona should be installed [$KUBE_NAMESPACE]")
	installCmd.Flags().StringVar(&installCmdOptions.kube.context, "kube-context-name", viper.GetString("kube-context"), "Name of the kubernetes context on which venona should be installed (default is current-context) [$KUBE_CONTEXT]")
	installCmd.Flags().StringVar(&installCmdOptions.storageClass, "storage-class", "", "Set a name of your custom storage class, note: this will not install volume provisioning components")
	installCmd.Flags().StringVar(&installCmdOptions.kube.nodeSelector, "kube-node-selector", "", "The kubernetes node selector \"key=value\" to be used by venona resources (default is no node selector)")
	installCmd.Flags().StringVar(&installCmdOptions.buildNodeSelector, "build-node-selector", "", "The kubernetes node selector \"key=value\" to be used by venona build resources (default is no node selector)")
	installCmd.Flags().StringArrayVar(&installCmdOptions.buildAnnotations, "build-annotations", []string{}, "The kubernetes metadata.annotations as \"key=value\" to be used by venona build resources (default is no node selector)")

	installCmd.Flags().BoolVar(&installCmdOptions.skipRuntimeInstallation, "skip-runtime-installation", false, "Set flag if you already have a configured runtime-environment, add --runtime-environment flag with name")
	installCmd.Flags().BoolVar(&installCmdOptions.kube.inCluster, "in-cluster", false, "Set flag if venona is been installed from inside a cluster")
	installCmd.Flags().BoolVar(&installCmdOptions.installOnlyRuntimeEnvironment, "only-runtime-environment", false, "Set to true to onlky configure namespace as runtime-environment for Codefresh")
	installCmd.Flags().BoolVar(&installCmdOptions.dryRun, "dry-run", false, "Set to true to simulate installation")
	installCmd.Flags().BoolVar(&installCmdOptions.setDefaultRuntime, "set-default", false, "Mark the install runtime-environment as default one after installation")
	installCmd.Flags().BoolVar(&installCmdOptions.kubernetesRunnerType, "kubernetes-runner-type", false, "Set the runner type to kubernetes (alpha feature)")

}

type nodeSelector map[string]string

func parseNodeSelector(s string) (nodeSelector, error) {
	if s == "" {
		return nodeSelector{}, nil
	}
	v := strings.Split(s, "=")
	if len(v) != 2 {
		return nil, errors.New("node selector must be in form \"key=value\"")
	}
	return nodeSelector{v[0]: v[1]}, nil
}

func validateInstallOptions(opts* plugins.InstallOptions) (error)  {
	if len(opts.ClusterName) > clusterNameMaxLength {
		return errors.New(fmt.Sprintf("cluster name length is limited to %d", clusterNameMaxLength))
	}
	if len(opts.ClusterNamespace) > namespaceMaxLength {
		return errors.New(fmt.Sprintf("cluster namespace length is limited to %d", namespaceMaxLength))
	} 	
	return nil
}

// String returns a k8s compliant string representation of the nodeSelector. Only a single value is supported.
func (ns nodeSelector) String() string {
	var s string
	for k, v := range ns {
		s = fmt.Sprintf("%s: %s", k, v)
	}
	return s
}
