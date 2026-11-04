package antigravity

import (
	"github.com/chenhg5/cc-connect/core"
	"reflect"
	"testing"
	"time"
)

func TestWorkspaceAgentOptionsPreservesStreamAndLauncher(t *testing.T) {
	original, err := New(map[string]any{"cmd": []string{"echo", "--custom-flag"}, "output_format": "stream-json", "mode": "yolo", "model": "native-model", "timeout_mins": 17, "env": map[string]string{"TEST_OPTION": "value=with=equals"}, "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, ok := original.(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("workspace creation loses the launcher and stream-json configuration")
	}
	opts := snapshotter.WorkspaceAgentOptions()
	if _, ok := opts["work_dir"]; ok {
		t.Fatal("snapshot carries the old workspace")
	}
	opts["work_dir"] = t.TempDir()
	cloned, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	a := original.(*Agent)
	b := cloned.(*Agent)
	if b.cmd != a.cmd || !reflect.DeepEqual(b.cliExtraArgs, a.cliExtraArgs) {
		t.Fatalf("launcher/stream arguments lost: %q %v", b.cmd, b.cliExtraArgs)
	}
	if b.mode != "yolo" || b.model != "native-model" || b.timeout != 17*time.Minute {
		t.Fatalf("runtime settings lost: %+v", opts)
	}
	if !reflect.DeepEqual(b.configEnv, a.configEnv) {
		t.Fatalf("environment lost: %v", b.configEnv)
	}
	if b.workDir == a.workDir {
		t.Fatal("workspace did not change")
	}
	opts["cmd"].([]string)[0] = "changed"
	opts["env"].(map[string]string)["TEST_OPTION"] = "changed"
	if a.cmd != "echo" || a.cliExtraArgs[0] != "--custom-flag" || a.configEnv[0] != "TEST_OPTION=value=with=equals" {
		t.Fatal("snapshot aliases original configuration")
	}
}
