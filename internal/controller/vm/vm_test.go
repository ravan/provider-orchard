/*
Copyright 2025 The Crossplane Authors.

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

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/ravan/provider-orchard/apis/compute/v1alpha1"
	apisv1alpha1 "github.com/ravan/provider-orchard/apis/v1alpha1"
	orchardclient "github.com/ravan/provider-orchard/internal/clients/orchard"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

// Mock HTTP client for testing
type mockHTTPClient struct {
	DoFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.DoFunc(req)
}

// newMockOrchardClient creates an OrchardClient with a mock HTTP client for testing
func newMockOrchardClient(httpClient *mockHTTPClient) *orchardclient.OrchardClient {
	client, err := orchardclient.NewClientWithResponses(
		"http://localhost:6120",
		orchardclient.WithHTTPClient(httpClient),
	)
	if err != nil {
		panic(err)
	}
	return &orchardclient.OrchardClient{
		ClientWithResponses: client,
	}
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	testToken := "test-token"
	testBaseURL := "http://localhost:8080"

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		kube   client.Client
		args   args
		want   want
	}{
		"SuccessfulConnectWithProviderConfig": {
			reason: "Should successfully connect with ProviderConfig",
			kube: &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *apisv1alpha1.ProviderConfig:
						o.Spec.Credentials = apisv1alpha1.ProviderCredentials{
							Source: xpv1.CredentialsSourceSecret,
							CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
								SecretRef: &xpv1.SecretKeySelector{
									SecretReference: xpv1.SecretReference{
										Name:      "test-secret",
										Namespace: "default",
									},
									Key: "credentials",
								},
							},
						}
						o.Spec.BaseURL = testBaseURL
						return nil
					case *corev1.Secret:
						o.Data = map[string][]byte{
							"credentials": []byte(`{"token":"` + testToken + `"}`),
						}
						return nil
					}
					return nil
				}),
				MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-vm",
							Namespace: "default",
						},
					}
					vm.SetProviderConfigReference(&xpv1.ProviderConfigReference{
						Name: "test-config",
						Kind: "ProviderConfig",
					})
					return vm
				}(),
			},
			want: want{
				err: nil,
			},
		},
		"MissingTokenError": {
			reason: "Should return error if token is missing from credentials",
			kube: &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *apisv1alpha1.ProviderConfig:
						o.Spec.Credentials = apisv1alpha1.ProviderCredentials{
							Source: xpv1.CredentialsSourceSecret,
							CommonCredentialSelectors: xpv1.CommonCredentialSelectors{
								SecretRef: &xpv1.SecretKeySelector{
									SecretReference: xpv1.SecretReference{
										Name:      "test-secret",
										Namespace: "default",
									},
									Key: "credentials",
								},
							},
						}
						o.Spec.BaseURL = testBaseURL
						return nil
					case *corev1.Secret:
						o.Data = map[string][]byte{
							"credentials": []byte(`{"other":"value"}`),
						}
						return nil
					}
					return nil
				}),
				MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-vm",
							Namespace: "default",
						},
					}
					vm.SetProviderConfigReference(&xpv1.ProviderConfigReference{
						Name: "test-config",
						Kind: "ProviderConfig",
					})
					return vm
				}(),
			},
			want: want{
				err: errors.New(errParseToken),
			},
		},
		"GetProviderConfigError": {
			reason: "Should return error if ProviderConfig cannot be fetched",
			kube: &test.MockClient{
				MockGet: test.NewMockGetFn(errBoom),
				MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-vm",
							Namespace: "default",
						},
					}
					vm.SetProviderConfigReference(&xpv1.ProviderConfigReference{
						Name: "test-config",
						Kind: "ProviderConfig",
					})
					return vm
				}(),
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errors.Wrap(errBoom, "cannot get object"), "cannot apply ProviderConfigUsage"), errTrackPCUsage),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &connector{
				kube:  tc.kube,
				usage: resource.NewProviderConfigUsageTracker(tc.kube, &apisv1alpha1.ProviderConfigUsage{}),
			}
			_, err := c.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nc.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	vmName := "test-vm"
	vmImage := "ubuntu:22.04"
	vmStatus := orchardclient.VMStatus("running")
	vmWorker := "worker-1"
	vmGeneration := float32(1)
	vmObservedGeneration := float32(1)

	type fields struct {
		client *orchardclient.OrchardClient
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NoExternalName": {
			reason: "Should return ResourceExists=false if external name is not set",
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
				},
			},
			want: want{
				o:   managed.ExternalObservation{ResourceExists: false},
				err: nil,
			},
		},
		"VMNotFound": {
			reason: "Should return ResourceExists=false if VM doesn't exist in Orchard",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				o:   managed.ExternalObservation{ResourceExists: false},
				err: nil,
			},
		},
		"VMExistsAndUpToDate": {
			reason: "Should return ResourceExists=true and ResourceUpToDate=true if VM exists and matches spec",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						vm := orchardclient.VM{
							Image:              &vmImage,
							Status:             &vmStatus,
							Worker:             &vmWorker,
							Generation:         &vmGeneration,
							ObservedGeneration: &vmObservedGeneration,
						}
						body, _ := json.Marshal(vm)
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBuffer(body)),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"VMExistsButOutdated": {
			reason: "Should return ResourceExists=true and ResourceUpToDate=false if VM exists but spec differs",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						oldImage := "ubuntu:20.04"
						vm := orchardclient.VM{
							Image:              &oldImage,
							Status:             &vmStatus,
							Worker:             &vmWorker,
							Generation:         &vmGeneration,
							ObservedGeneration: &vmObservedGeneration,
						}
						body, _ := json.Marshal(vm)
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBuffer(body)),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{client: tc.fields.client}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	vmName := "test-vm"
	vmImage := "ubuntu:22.04"

	type fields struct {
		client *orchardclient.OrchardClient
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		c   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"SuccessfulCreate": {
			reason: "Should successfully create VM",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusCreated,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
					Spec: v1alpha1.VMSpec{
						ForProvider: v1alpha1.VMParameters{
							Image: vmImage,
						},
					},
				},
			},
			want: want{
				c:   managed.ExternalCreation{},
				err: nil,
			},
		},
		"ConflictHandled": {
			reason: "Should handle 409 conflict gracefully",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusConflict,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
					Spec: v1alpha1.VMSpec{
						ForProvider: v1alpha1.VMParameters{
							Image: vmImage,
						},
					},
				},
			},
			want: want{
				c:   managed.ExternalCreation{},
				err: nil,
			},
		},
		"CreateError": {
			reason: "Should return error on unexpected status code",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
					Spec: v1alpha1.VMSpec{
						ForProvider: v1alpha1.VMParameters{
							Image: vmImage,
						},
					},
				},
			},
			want: want{
				c:   managed.ExternalCreation{},
				err: errors.Errorf("unexpected status code creating VM: %d", http.StatusInternalServerError),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{client: tc.fields.client}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	vmName := "test-vm"
	vmImage := "ubuntu:22.04"

	type fields struct {
		client *orchardclient.OrchardClient
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		u   managed.ExternalUpdate
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NoExternalNameError": {
			reason: "Should return error if external name is not set",
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
					Spec: v1alpha1.VMSpec{
						ForProvider: v1alpha1.VMParameters{
							Image: vmImage,
						},
					},
				},
			},
			want: want{
				u:   managed.ExternalUpdate{},
				err: errors.New("external name not set"),
			},
		},
		"SuccessfulUpdate": {
			reason: "Should successfully update VM",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				u:   managed.ExternalUpdate{},
				err: nil,
			},
		},
		"UpdateError": {
			reason: "Should return error on unexpected status code",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusBadRequest,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				u:   managed.ExternalUpdate{},
				err: errors.Errorf("unexpected status code updating VM: %d", http.StatusBadRequest),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{client: tc.fields.client}
			got, err := e.Update(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Update(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.u, got); diff != "" {
				t.Errorf("\n%s\ne.Update(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	vmName := "test-vm"
	vmImage := "ubuntu:22.04"

	type fields struct {
		client *orchardclient.OrchardClient
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NoExternalName": {
			reason: "Should return nil if external name is not set (nothing to delete)",
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.VM{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
					},
					Spec: v1alpha1.VMSpec{
						ForProvider: v1alpha1.VMParameters{
							Image: vmImage,
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"SuccessfulDelete": {
			reason: "Should successfully delete VM",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				err: nil,
			},
		},
		"AlreadyDeleted": {
			reason: "Should handle 404 gracefully (already deleted)",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				err: nil,
			},
		},
		"DeleteError": {
			reason: "Should return error on unexpected status code",
			fields: fields{
				client: newMockOrchardClient(&mockHTTPClient{
					DoFunc: func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Body:       io.NopCloser(bytes.NewBufferString("")),
						}, nil
					},
				}),
			},
			args: args{
				ctx: context.Background(),
				mg: func() resource.Managed {
					vm := &v1alpha1.VM{
						ObjectMeta: metav1.ObjectMeta{
							Name: vmName,
						},
						Spec: v1alpha1.VMSpec{
							ForProvider: v1alpha1.VMParameters{
								Image: vmImage,
							},
						},
					}
					meta.SetExternalName(vm, vmName)
					return vm
				}(),
			},
			want: want{
				err: errors.Errorf("unexpected status code deleting VM: %d", http.StatusInternalServerError),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &external{client: tc.fields.client}
			_, err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestBuildVMSpec(t *testing.T) {
	vmImage := "ubuntu:22.04"
	cpu := int32(2)
	memory := int32(4096)
	diskSize := int32(20480)
	username := "admin"
	password := "password"

	cases := map[string]struct {
		reason string
		params *v1alpha1.VMParameters
		want   *orchardclient.VMSpec
	}{
		"MinimalSpec": {
			reason: "Should build minimal VM spec with just image",
			params: &v1alpha1.VMParameters{
				Image: vmImage,
			},
			want: &orchardclient.VMSpec{
				Image: &vmImage,
			},
		},
		"FullSpec": {
			reason: "Should build complete VM spec with all parameters",
			params: &v1alpha1.VMParameters{
				Image:    vmImage,
				CPU:      &cpu,
				Memory:   &memory,
				DiskSize: &diskSize,
				Username: &username,
				Password: &password,
			},
			want: func() *orchardclient.VMSpec {
				cpuFloat := float32(cpu)
				memFloat := float32(memory)
				diskFloat := float32(diskSize)
				return &orchardclient.VMSpec{
					Image:    &vmImage,
					Cpu:      &cpuFloat,
					Memory:   &memFloat,
					DiskSize: &diskFloat,
					Username: &username,
					Password: &password,
				}
			}(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := buildVMSpec(tc.params)
			if diff := cmp.Diff(tc.want.Image, got.Image); diff != "" {
				t.Errorf("\n%s\nbuildVMSpec(...).Image: -want, +got:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.Cpu, got.Cpu); diff != "" {
				t.Errorf("\n%s\nbuildVMSpec(...).Cpu: -want, +got:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.Memory, got.Memory); diff != "" {
				t.Errorf("\n%s\nbuildVMSpec(...).Memory: -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestIsVMUpToDate(t *testing.T) {
	vmImage := "ubuntu:22.04"
	oldImage := "ubuntu:20.04"
	cpu := float32(2)
	cpuInt := int32(2)
	memory := float32(4096)
	memoryInt := int32(4096)

	cases := map[string]struct {
		reason string
		params *v1alpha1.VMParameters
		vm     *orchardclient.VM
		want   bool
	}{
		"ImageMatches": {
			reason: "Should return true when image matches",
			params: &v1alpha1.VMParameters{
				Image: vmImage,
			},
			vm: &orchardclient.VM{
				Image: &vmImage,
			},
			want: true,
		},
		"ImageDiffers": {
			reason: "Should return false when image differs",
			params: &v1alpha1.VMParameters{
				Image: vmImage,
			},
			vm: &orchardclient.VM{
				Image: &oldImage,
			},
			want: false,
		},
		"CPUDiffers": {
			reason: "Should return false when CPU differs",
			params: &v1alpha1.VMParameters{
				Image: vmImage,
				CPU:   &cpuInt,
			},
			vm: &orchardclient.VM{
				Image: &vmImage,
				Cpu:   ptr(float32(4)),
			},
			want: false,
		},
		"MemoryDiffers": {
			reason: "Should return false when memory differs",
			params: &v1alpha1.VMParameters{
				Image:  vmImage,
				Memory: &memoryInt,
			},
			vm: &orchardclient.VM{
				Image:  &vmImage,
				Memory: ptr(float32(8192)),
			},
			want: false,
		},
		"AllFieldsMatch": {
			reason: "Should return true when all fields match",
			params: &v1alpha1.VMParameters{
				Image:  vmImage,
				CPU:    &cpuInt,
				Memory: &memoryInt,
			},
			vm: &orchardclient.VM{
				Image:  &vmImage,
				Cpu:    &cpu,
				Memory: &memory,
			},
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := isVMUpToDate(tc.params, tc.vm)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nisVMUpToDate(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}
