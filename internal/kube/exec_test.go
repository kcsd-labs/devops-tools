package kube

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func hostPathSpec(p string) *corev1.PodSpec {
	return &corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app"}},
		Volumes: []corev1.Volume{{Name: "v", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: p}}}},
	}
}

// The node's time zone is how most images get the local time; it reaches no
// further into the node. Anything else on the node does.
func TestTimeZoneMountsDoNotCloseAPodToAShell(t *testing.T) {
	for _, p := range []string{"/etc/localtime", "/etc/timezone", "/usr/share/zoneinfo", "/usr/share/zoneinfo/Asia/Almaty"} {
		if r := nodeAccess(hostPathSpec(p)); r != "" {
			t.Errorf("%s: refused (%s)", p, r)
		}
	}
	for _, p := range []string{"/", "/etc", "/var/run/docker.sock", "/var/lib/kubelet", "/usr/share/zoneinfo/../../../etc", "/etc/localtime/.."} {
		if r := nodeAccess(hostPathSpec(p)); !strings.Contains(r, "hostPath") {
			t.Errorf("%s: allowed", p)
		}
	}
}

func TestShellProbe(t *testing.T) {
	info, err := parseShellProbe("/usr/bin/bash\nCP1251\n", "", nil)
	if err != nil || info.Path != "/usr/bin/bash" || info.Charset != "CP1251" {
		t.Errorf("got %+v, %v", info, err)
	}
	info, err = parseShellProbe("/bin/sh\n", "", nil)
	if err != nil || info.Path != "/bin/sh" || info.Charset != "" {
		t.Errorf("no locale command: got %+v, %v", info, err)
	}
	if _, err := parseShellProbe("", "exec: \"sh\": executable file not found in $PATH", errors.New("command terminated")); err == nil {
		t.Error("no sh should be an error")
	}
}
