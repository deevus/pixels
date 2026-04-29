package truenas

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/ssh"
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

func TestNewForTestWiresFilesViaExec(t *testing.T) {
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{},
	})
	if tn.FilesViaExec.Exec == nil {
		t.Fatal("NewForTest: FilesViaExec.Exec is nil; should be the TrueNAS instance itself")
	}
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
			if inst.Status != sandbox.StatusRunning {
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
	if instances[0].Status != sandbox.StatusRunning {
		t.Errorf("instances[0].Status = %q", instances[0].Status)
	}
}

func TestStop(t *testing.T) {
	var stopCalled bool
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
				return &tnapi.VirtInstance{Name: name, Status: "RUNNING"}, nil
			},
			StopInstanceFunc: func(ctx context.Context, name string, opts tnapi.StopVirtInstanceOpts) error {
				stopCalled = true
				if name != "px-test" {
					t.Errorf("stop called with %q", name)
				}
				if opts.Timeout != stopTimeoutSeconds {
					t.Errorf("timeout = %d, want %d", opts.Timeout, stopTimeoutSeconds)
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
				GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
					return &tnapi.VirtInstance{Name: name, Status: "RUNNING"}, nil
				},
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
		if err := tn.Delete(context.Background(), "test"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !deleteCalled {
			t.Error("delete not called")
		}
	})

	t.Run("retry on error", func(t *testing.T) {
		attempts := 0
		tn := newTestBackend(t, &Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
					return &tnapi.VirtInstance{Name: name, Status: "RUNNING"}, nil
				},
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

	t.Run("missing instance returns ErrNotFound", func(t *testing.T) {
		var deleteCalled bool
		tn := newTestBackend(t, &Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
					return nil, errors.New("VirtInstance px-test does not exist")
				},
				DeleteInstanceFunc: func(ctx context.Context, name string) error {
					deleteCalled = true
					return nil
				},
			},
		})

		err := tn.Delete(context.Background(), "test")
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Errorf("expected ErrNotFound; got %v", err)
		}
		if deleteCalled {
			t.Error("DeleteInstance should not be called when GetInstance reports missing")
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
	if inst.Status != sandbox.StatusRunning {
		t.Errorf("status = %q", inst.Status)
	}
	if len(inst.Addresses) != 1 || inst.Addresses[0] != "10.0.0.42" {
		t.Errorf("addresses = %v", inst.Addresses)
	}
}

func TestStart(t *testing.T) {
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
}

func TestCloneFrom(t *testing.T) {
	var createOpts tnapi.CreateVirtInstanceOpts
	var rootfsCmd string
	var startedClone string
	var deletedCronJobID int64

	mssh := &mockSSH{}
	cfg := testCfg()
	cfg["nic_type"] = "MACVLAN"
	cfg["parent"] = "br0"
	cfg["dataset_prefix"] = "tank/virt"

	tn, _ := NewForTest(&Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
				switch name {
				case "px-source":
					return &tnapi.VirtInstance{
						Name: "px-source", Status: "RUNNING",
						CPU: "4", Memory: 8192,
					}, nil
				case "px-newbox":
					// Reported as STOPPED so StopInstanceIfRunning is a no-op.
					return &tnapi.VirtInstance{Name: "px-newbox", Status: "STOPPED"}, nil
				}
				return nil, errors.New("unexpected GetInstance: " + name)
			},
			CreateInstanceFunc: func(ctx context.Context, opts tnapi.CreateVirtInstanceOpts) (*tnapi.VirtInstance, error) {
				createOpts = opts
				return &tnapi.VirtInstance{Name: opts.Name, Status: "STOPPED"}, nil
			},
			StopInstanceFunc: func(ctx context.Context, name string, opts tnapi.StopVirtInstanceOpts) error {
				t.Errorf("StopInstance should not be called for STOPPED clone shell; got name=%s", name)
				return nil
			},
			GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
				return &tnapi.VirtGlobalConfig{Dataset: "tank/ix-virt"}, nil
			},
			StartInstanceFunc: func(ctx context.Context, name string) error {
				startedClone = name
				return nil
			},
		},
		Cron: &tnapi.MockCronService{
			CreateFunc: func(ctx context.Context, opts tnapi.CreateCronJobOpts) (*tnapi.CronJob, error) {
				rootfsCmd = opts.Command
				return &tnapi.CronJob{ID: 42}, nil
			},
			DeleteFunc: func(ctx context.Context, id int64) error {
				deletedCronJobID = id
				return nil
			},
		},
	}, mssh, cfg)

	if err := tn.CloneFrom(context.Background(), "source", "snap1", "newbox"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if createOpts.Name != "px-newbox" {
		t.Errorf("CreateInstance Name = %q, want px-newbox", createOpts.Name)
	}
	if createOpts.CPU != "4" {
		t.Errorf("CreateInstance CPU = %q, want 4 (copied from source)", createOpts.CPU)
	}
	if createOpts.Memory != 8192 {
		t.Errorf("CreateInstance Memory = %d, want 8192 (copied from source)", createOpts.Memory)
	}
	if createOpts.Autostart {
		t.Error("CreateInstance Autostart should be false")
	}
	if len(createOpts.Devices) != 1 {
		t.Fatalf("CreateInstance Devices = %v, want one NIC", createOpts.Devices)
	}
	nic := createOpts.Devices[0]
	if nic.NICType != "MACVLAN" || nic.Parent != "br0" {
		t.Errorf("NIC = %+v, want MACVLAN/br0 from cfg", nic)
	}

	if !strings.Contains(rootfsCmd, "tank/virt/px-source@snap1") {
		t.Errorf("rootfs cmd should clone snap1 from prefixed source dataset; got %q", rootfsCmd)
	}
	if !strings.Contains(rootfsCmd, "px-newbox") {
		t.Errorf("rootfs cmd should target px-newbox; got %q", rootfsCmd)
	}
	if startedClone != "px-newbox" {
		t.Errorf("StartInstance called with %q, want px-newbox", startedClone)
	}
	if deletedCronJobID != 42 {
		t.Errorf("Cron.Delete called with id=%d, want 42 (cleanup of temp job)", deletedCronJobID)
	}
}

func TestCloneFromSourceNotFound(t *testing.T) {
	tn := newTestBackend(t, &Client{
		Virt: &tnapi.MockVirtService{
			GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
				return nil, errors.New("instance px-missing does not exist")
			},
		},
	})

	err := tn.CloneFrom(context.Background(), "missing", "snap1", "newbox")
	if err == nil {
		t.Fatal("expected error when source missing")
	}
	if !strings.Contains(err.Error(), "getting source missing") {
		t.Errorf("error %q should mention source name", err.Error())
	}
}

func TestWriteFile(t *testing.T) {
	t.Run("root-owned skips chown", func(t *testing.T) {
		var writePath string
		mssh := &mockSSH{}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
				},
			},
			Filesystem: &tnapi.MockFilesystemService{
				WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
					writePath = path
					return nil
				},
			},
		}, mssh, testCfg())

		err := tn.WriteFile(context.Background(), "test", "/etc/hello", []byte("hi"), 0o644, sandbox.NoOwner, sandbox.NoOwner)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if writePath == "" {
			t.Fatal("expected Filesystem.WriteFile to be called")
		}
		if !strings.HasSuffix(writePath, "/containers/px-test/rootfs/etc/hello") {
			t.Errorf("write path = %q; want suffix /containers/px-test/rootfs/etc/hello", writePath)
		}
		if len(mssh.execCalls) != 0 {
			t.Errorf("expected no SSH calls for root-owned write; got %v", mssh.execCalls)
		}
	})

	t.Run("uid/gid triggers chown over SSH", func(t *testing.T) {
		mssh := &mockSSH{}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
				},
				GetInstanceFunc: runningInstanceFunc("10.0.0.1"),
			},
			Filesystem: &tnapi.MockFilesystemService{
				WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
					return nil
				},
			},
		}, mssh, testCfg())

		err := tn.WriteFile(context.Background(), "test", "/home/pixel/file", []byte("x"), 0o600, 1000, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mssh.execCalls) != 1 {
			t.Fatalf("expected 1 SSH exec call; got %d (%v)", len(mssh.execCalls), mssh.execCalls)
		}
		got := mssh.execCalls[0]
		if got.Host != "px-test" {
			t.Errorf("host = %q, want px-test", got.Host)
		}
		if got.User != "root" {
			t.Errorf("user = %q, want root (Root: true)", got.User)
		}
		want := []string{"chown", "--", "1000:1000", "/home/pixel/file"}
		if len(got.Cmd) != len(want) {
			t.Fatalf("chown cmd = %v, want %v", got.Cmd, want)
		}
		for i := range want {
			if got.Cmd[i] != want[i] {
				t.Errorf("chown cmd[%d] = %q, want %q", i, got.Cmd[i], want[i])
			}
		}
	})

	t.Run("WriteContainerFile error short-circuits", func(t *testing.T) {
		mssh := &mockSSH{}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
				},
			},
			Filesystem: &tnapi.MockFilesystemService{
				WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
					return errors.New("disk full")
				},
			},
		}, mssh, testCfg())

		err := tn.WriteFile(context.Background(), "test", "/x", []byte("x"), 0o644, 1000, 1000)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if len(mssh.execCalls) != 0 {
			t.Errorf("chown should not run on WriteContainerFile error; got %v", mssh.execCalls)
		}
	})

	t.Run("chown failure surfaces", func(t *testing.T) {
		mssh := &mockSSH{
			execFn: func(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
				return 1, errors.New("ssh boom")
			},
		}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
				},
				GetInstanceFunc: runningInstanceFunc("10.0.0.1"),
			},
			Filesystem: &tnapi.MockFilesystemService{
				WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
					return nil
				},
			},
		}, mssh, testCfg())

		err := tn.WriteFile(context.Background(), "test", "/x", []byte("x"), 0o644, 1000, 1000)
		if err == nil {
			t.Fatal("expected chown error, got nil")
		}
		if !strings.Contains(err.Error(), "chown") {
			t.Errorf("error %q should mention chown", err.Error())
		}
	})
}

func TestReady(t *testing.T) {
	t.Run("happy path: RUNNING with IP, auth ok", func(t *testing.T) {
		mssh := &mockSSH{}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			},
		}, mssh, testCfg())

		err := tn.Ready(context.Background(), "test", 5*time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mssh.waitCalls) != 1 || mssh.waitCalls[0] != "px-test" {
			t.Errorf("expected one WaitReady call for px-test; got %v", mssh.waitCalls)
		}
		if len(mssh.testAuthCalls) != 1 {
			t.Errorf("expected one TestAuth call; got %d", len(mssh.testAuthCalls))
		}
	})

	t.Run("polls until RUNNING with IP", func(t *testing.T) {
		mssh := &mockSSH{}
		var calls int
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
					calls++
					switch calls {
					case 1:
						return &tnapi.VirtInstance{Name: name, Status: "STARTING"}, nil
					case 2:
						return &tnapi.VirtInstance{Name: name, Status: "RUNNING"}, nil // no IP yet
					default:
						return &tnapi.VirtInstance{
							Name: name, Status: "RUNNING",
							Aliases: []tnapi.VirtAlias{{Type: "INET", Address: "10.0.0.5"}},
						}, nil
					}
				},
			},
		}, mssh, testCfg())

		// Need timeout > 2 polling intervals (2s) but tight enough to fail fast.
		err := tn.Ready(context.Background(), "test", 5*time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls < 3 {
			t.Errorf("expected at least 3 GetInstance calls (poll loop); got %d", calls)
		}
	})

	t.Run("auth failure pushes pubkey", func(t *testing.T) {
		mssh := &mockSSH{
			testAuthFn: func(ctx context.Context, cc ssh.ConnConfig) error {
				return errors.New("permission denied")
			},
		}

		dir := t.TempDir()
		keyPath := dir + "/id"
		if err := os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o600); err != nil {
			t.Fatalf("writing fake pub key: %v", err)
		}

		var writeCalls int
		cfg := testCfg()
		cfg["ssh_key"] = keyPath

		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
				GetGlobalConfigFunc: func(ctx context.Context) (*tnapi.VirtGlobalConfig, error) {
					return &tnapi.VirtGlobalConfig{Pool: "tank"}, nil
				},
			},
			Filesystem: &tnapi.MockFilesystemService{
				WriteFileFunc: func(ctx context.Context, path string, params tnapi.WriteFileParams) error {
					writeCalls++
					return nil
				},
			},
		}, mssh, cfg)

		if err := tn.Ready(context.Background(), "test", 5*time.Second); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// WriteAuthorizedKey writes to both root and pixel authorized_keys.
		if writeCalls != 2 {
			t.Errorf("expected 2 Filesystem.WriteFile calls (root + pixel); got %d", writeCalls)
		}
	})

	t.Run("auth failure with no pubkey errors", func(t *testing.T) {
		mssh := &mockSSH{
			testAuthFn: func(ctx context.Context, cc ssh.ConnConfig) error {
				return errors.New("permission denied")
			},
		}
		cfg := testCfg()
		cfg["ssh_key"] = t.TempDir() + "/nonexistent"

		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: runningInstanceFunc("10.0.0.5"),
			},
		}, mssh, cfg)

		err := tn.Ready(context.Background(), "test", 5*time.Second)
		if err == nil {
			t.Fatal("expected error when auth fails and no public key file exists")
		}
		if !strings.Contains(err.Error(), "no public key") {
			t.Errorf("error %q should mention missing public key", err.Error())
		}
	})

	t.Run("instance never appears: deadline exceeded", func(t *testing.T) {
		mssh := &mockSSH{}
		tn, _ := NewForTest(&Client{
			Virt: &tnapi.MockVirtService{
				GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
					// Never reaches RUNNING.
					return &tnapi.VirtInstance{Name: name, Status: "STOPPED"}, nil
				},
			},
		}, mssh, testCfg())

		// Tight timeout so the test finishes quickly.
		err := tn.Ready(context.Background(), "test", 1500*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.Contains(err.Error(), "deadline exceeded") {
			t.Errorf("error %q should mention deadline exceeded", err.Error())
		}
	})
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
