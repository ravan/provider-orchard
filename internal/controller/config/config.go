/*
Copyright 2020 The Crossplane Authors.

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

package config

import (
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/providerconfig"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ravan/provider-orchard/apis/v1alpha1"
)

// SetupGated adds ProviderConfig controllers with safe-start support.
// Controllers are gated behind their respective Usage CRDs being established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	// Gate ProviderConfig controller behind ProviderConfigUsage CRD
	o.Gate.Register(func() {
		if err := setupProviderConfig(
			mgr, o,
			v1alpha1.ProviderConfigGroupKind,
			resource.ProviderConfigKinds{
				Config:    v1alpha1.ProviderConfigGroupVersionKind,
				Usage:     v1alpha1.ProviderConfigUsageGroupVersionKind,
				UsageList: v1alpha1.ProviderConfigUsageListGroupVersionKind,
			},
			&v1alpha1.ProviderConfig{},
			&v1alpha1.ProviderConfigUsage{},
		); err != nil {
			panic(errors.Wrap(err, "cannot setup ProviderConfig controller"))
		}
	}, v1alpha1.ProviderConfigUsageGroupVersionKind)

	// Gate ClusterProviderConfig controller behind ClusterProviderConfigUsage CRD
	o.Gate.Register(func() {
		if err := setupProviderConfig(
			mgr, o,
			v1alpha1.ClusterProviderConfigGroupKind,
			resource.ProviderConfigKinds{
				Config:    v1alpha1.ClusterProviderConfigGroupVersionKind,
				Usage:     v1alpha1.ClusterProviderConfigUsageGroupVersionKind,
				UsageList: v1alpha1.ClusterProviderConfigUsageListGroupVersionKind,
			},
			&v1alpha1.ClusterProviderConfig{},
			&v1alpha1.ClusterProviderConfigUsage{},
		); err != nil {
			panic(errors.Wrap(err, "cannot setup ClusterProviderConfig controller"))
		}
	}, v1alpha1.ClusterProviderConfigUsageGroupVersionKind)

	return nil
}

// Setup adds a controller that reconciles ProviderConfigs by accounting for
// their current usage.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	if err := setupProviderConfig(
		mgr, o,
		v1alpha1.ProviderConfigGroupKind,
		resource.ProviderConfigKinds{
			Config:    v1alpha1.ProviderConfigGroupVersionKind,
			Usage:     v1alpha1.ProviderConfigUsageGroupVersionKind,
			UsageList: v1alpha1.ProviderConfigUsageListGroupVersionKind,
		},
		&v1alpha1.ProviderConfig{},
		&v1alpha1.ProviderConfigUsage{},
	); err != nil {
		return err
	}

	return setupProviderConfig(
		mgr, o,
		v1alpha1.ClusterProviderConfigGroupKind,
		resource.ProviderConfigKinds{
			Config:    v1alpha1.ClusterProviderConfigGroupVersionKind,
			Usage:     v1alpha1.ClusterProviderConfigUsageGroupVersionKind,
			UsageList: v1alpha1.ClusterProviderConfigUsageListGroupVersionKind,
		},
		&v1alpha1.ClusterProviderConfig{},
		&v1alpha1.ClusterProviderConfigUsage{},
	)
}

func setupProviderConfig(
	mgr ctrl.Manager,
	o controller.Options,
	groupKind string,
	kinds resource.ProviderConfigKinds,
	configObj client.Object,
	usageObj client.Object,
) error {
	name := providerconfig.ControllerName(groupKind)

	r := providerconfig.NewReconciler(mgr, kinds,
		providerconfig.WithLogger(o.Logger.WithValues("controller", name)),
		providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(configObj).
		Watches(usageObj, &resource.EnqueueRequestForProviderConfig{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}
