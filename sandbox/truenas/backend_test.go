package truenas

import (
	"context"
	"errors"
	"strings"
	"testing"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/sandbox"
)

// testCfg returns a minimal valid config map for NewForTest.
func testCfg() map[string]string {
	return map[string]string{
		"host":      "nas.test",
		"api_key":   "test-key",
		"provision": "false",
	}
}

// newTestBackend creates a TrueNAS backend with mock services.
func newTestBackend(t *testing.T, client *Client) *TrueNAS {
	t.Helper()
	tn, err := NewForTest(client, &mockSSH{}, testCfg())
	if err != nil {
		t.Fatalf("NewForTest: %v", err)
	}
	return tn
}

func TestGet(t *testing.T) {
	tests := []struct {
		name     string
		instance *tnapi.VirtInstance
		getErr   error
		wantName string
		wantErr  string
	}{
		{
			name: "found",
			instance: &tnapi.VirtInstance{
				Name:   "px-mybox",
				Status: "RUNNING",
				Aliases: []tnapi.VirtAlias{
					{Type: "INET", Address: "10.0.0.5"},
				},
			},
			wantName: "mybox",
		},
		{
			name:    "not found",
			wantErr: "not found",
		},
		{
			name:    "API error",
			getErr:  errors.New("connection failed"),
			wantErr: "getting mybox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tn := newTestBackend(t, &Client{
				Virt: &tnapi.MockVirtService{
					GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
						if name != "px-mybox" {
							t.Errorf("GetInstance called with %q, want px-mybox", name)
						}
						if tt.getErr != nil {
							return nil, tt.getErr
						}
						return tt.instance, nil
					},
				},
			})

			inst, err := tn.Get(context.Background(), "mybox")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inst.Name != tt.wantName {
				t.Errorf("name = %q, want %q", inst.Name, tt.wantName)
			}
			if inst.Status != "RUNNING" {
				t.Errorf("status = %q", inst.Status)
			}
			if len(inst.Addresses) != 1 || inst.Addresses[0] != "10.0.0.5" {
				t.Errorf("addresses = %v", inst.Addresses)
			}
		})
	}
}

func TestList(t *testing.T) {
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			ListInstancesFunc: func(ctx context.Context, filters [][]any) ([]tnapi.VirtInstance, error) {
				return []tnapi.VirtInstance{
					{Name: "px-alpha", Status: "RUNNING", Aliases: []tnapi.VirtAlias{{Type: "INET", Address: "10.0.0.1"}}},
					{Name: "px-beta", Status: "STOPPED"},
				}, nil
			},
		},
	})

	instances, err := tn.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("got %d instances, want 2", len(instances))
	}
	if instances[0].Name != "alpha" {
		t.Errorf("instances[0].Name = %q, want alpha", instances[0].Name)
	}
	if instances[1].Name != "beta" {
		t.Errorf("instances[1].Name = %q, want beta", instances[1].Name)
	}
	if instances[0].Status != "RUNNING" {
		t.Errorf("instances[0].Status = %q", instances[0].Status)
	}
}

func TestStop(t *testing.T) {
	var stopCalled bool
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			StopInstanceFunc: func(ctx context.Context, name string, opts tnapi.StopVirtInstanceOpts) error {
				stopCalled = true
				if name != "px-test" {
					t.Errorf("stop called with %q", name)
				}
				if opts.Timeout != 30 {
					t.Errorf("timeout = %d, want 30", opts.Timeout)
				}
				return nil
			},
		},
	})

	if err := tn.Stop(context.Background(), "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopCalled {
		t.Error("stop not called")
	}
}

func TestDelete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var deleteCalled bool
		tn := newTestBackend(t, &Client{
			Virt: &tnapi.MockVirtService{
				StopInstanceFunc: func(ctx context.Context, name string, opts tnapi.StopVirtInstanceOpts) error {
					return nil
				},
				DeleteInstanceFunc: func(ctx context.Context, name string) error {
					deleteCalled = true
					if name != "px-test" {
						t.Errorf("delete called with %q", name)
					}
					return nil
				},
			},
		})
		cache.Put("test", &cache.Entry{IP: "10.0.0.5", Status: "RUNNING"})
		defer cache.Delete("test")

		if err := tn.Delete(context.Background(), "test"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !deleteCalled {
			t.Error("delete not called")
		}
		if cached := cache.Get("test"); cached != nil {
			t.Error("cache should be evicted")
		}
	})

	t.Run("retry on error", func(t *testing.T) {
		attempts := 0
		tn := newTestBackend(t, &Client{
			Virt: &tnapi.MockVirtService{
				StopInstanceFunc: func(ctx context.Context, name string, opts tnapi.StopVirtInstanceOpts) error {
					return nil
				},
				DeleteInstanceFunc: func(ctx context.Context, name string) error {
					attempts++
					if attempts < 3 {
						return errors.New("storage busy")
					}
					return nil
				},
			},
		})

		if err := tn.Delete(context.Background(), "test"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
	})
}

func TestCreateSnapshot(t *testing.T) {
	var created tnapi.CreateSnapshotOpts
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Dataset: "tank/ix-virt"}, nil
			},
		},
		Snapshot: &tnapi.MockSnapshotService{
			CreateFunc: func(ctx context.Context, opts tnapi.CreateSnapshotOpts) (*tnapi.Snapshot, error) {
				created = opts
				return &tnapi.Snapshot{}, nil
			},
		},
	})

	if err := tn.CreateSnapshot(context.Background(), "test", "snap1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Dataset != "tank/ix-virt/containers/px-test" {
		t.Errorf("dataset = %q", created.Dataset)
	}
	if created.Name != "snap1" {
		t.Errorf("name = %q", created.Name)
	}
}

func TestListSnapshots(t *testing.T) {
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Dataset: "tank/ix-virt"}, nil
			},
		},
		Snapshot: &tnapi.MockSnapshotService{
			QueryFunc: func(ctx context.Context, filters [][]any) ([]tnapi.Snapshot, error) {
				return []tnapi.Snapshot{
					{SnapshotName: "snap1", Referenced: 1024},
					{SnapshotName: "snap2", Referenced: 2048},
				}, nil
			},
		},
	})

	snaps, err := tn.ListSnapshots(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	if snaps[0].Label != "snap1" || snaps[0].Size != 1024 {
		t.Errorf("snap[0] = %+v", snaps[0])
	}
	if snaps[1].Label != "snap2" || snaps[1].Size != 2048 {
		t.Errorf("snap[1] = %+v", snaps[1])
	}
}

func TestDeleteSnapshot(t *testing.T) {
	var deletedID string
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Dataset: "tank/ix-virt"}, nil
			},
		},
		Snapshot: &tnapi.MockSnapshotService{
			DeleteFunc: func(ctx context.Context, id string) error {
				deletedID = id
				return nil
			},
		},
	})

	if err := tn.DeleteSnapshot(context.Background(), "test", "snap1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "tank/ix-virt/containers/px-test@snap1"
	if deletedID != want {
		t.Errorf("deleted = %q, want %q", deletedID, want)
	}
}

func TestResolveDataset(t *testing.T) {
	t.Run("with prefix override", func(t *testing.T) {
		tn, _ := NewForTest(&Client{}, &mockSSH{}, map[string]string{
			"host":           "nas.test",
			"api_key":        "key",
			"dataset_prefix": "mypool/virt",
			"provision":      "false",
		})

		ds, err := tn.resolveDataset(context.Background(), "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ds != "mypool/virt/px-test" {
			t.Errorf("dataset = %q", ds)
		}
	})

	t.Run("auto-detect from API", func(t *testing.T) {
		tn := newTestBackend(t, &Client{
			Virt: &tnapi.MockVirtService{
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Dataset: "tank/ix-virt"}, nil
				},
			},
		})

		ds, err := tn.resolveDataset(context.Background(), "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ds != "tank/ix-virt/containers/px-test" {
			t.Errorf("dataset = %q", ds)
		}
	})
}

func TestCreateNoProvision(t *testing.T) {
	cache.Delete("test")
	defer cache.Delete("test")

	mssh := &mockSSH{}
	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			CreateInstanceFunc: func(ctx context.Context, opts tnapi.CreateVirtInstanceOpts) (*tnapi.VirtInstance, error) {
				return &tnapi.VirtInstance{
					Name:   opts.Name,
					Status: "RUNNING",
					Aliases: []tnapi.VirtAlias{
						{Type: "INET", Address: "10.0.0.42"},
					},
				}, nil
			},
			GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
				return &tnapi.VirtInstance{
					Name:   name,
					Status: "RUNNING",
					Aliases: []tnapi.VirtAlias{
						{Type: "INET", Address: "10.0.0.42"},
					},
				}, nil
			},
		},
		Interface: &tnapi.MockInterfaceService{},
		Network:   &tnapi.MockNetworkService{},
	}, mssh, testCfg())

	inst, err := tn.Create(context.Background(), sandbox.CreateOpts{Name: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.Name != "test" {
		t.Errorf("name = %q", inst.Name)
	}
	if inst.Status != "RUNNING" {
		t.Errorf("status = %q", inst.Status)
	}
	if len(inst.Addresses) != 1 || inst.Addresses[0] != "10.0.0.42" {
		t.Errorf("addresses = %v", inst.Addresses)
	}

	// Verify cached.
	cached := cache.Get("test")
	if cached == nil || cached.IP != "10.0.0.42" {
		t.Errorf("cache = %+v", cached)
	}
}

func TestStart(t *testing.T) {
	cache.Delete("test")
	defer cache.Delete("test")

	mssh := &mockSSH{}
	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			StartInstanceFunc: func(ctx context.Context, name string) error {
				if name != "px-test" {
					t.Errorf("start called with %q", name)
				}
				return nil
			},
			GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
				return &tnapi.VirtInstance{
					Name:   name,
					Status: "RUNNING",
					Aliases: []tnapi.VirtAlias{{Type: "INET", Address: "10.0.0.7"}},
				}, nil
			},
		},
	}, mssh, testCfg())

	if err := tn.Start(context.Background(), "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cached := cache.Get("test")
	if cached == nil || cached.IP != "10.0.0.7" {
		t.Errorf("cache = %+v", cached)
	}
}

func TestCapabilities(t *testing.T) {
	tn := &TrueNAS{}
	caps := tn.Capabilities()
	if !caps.Snapshots {
		t.Error("Snapshots should be true")
	}
	if !caps.CloneFrom {
		t.Error("CloneFrom should be true")
	}
	if !caps.EgressControl {
		t.Error("EgressControl should be true")
	}
}
