package registry

import "testing"

func TestCheckAgentAvailabilityForAgent(t *testing.T) {
	t.Run("binary_on_path", func(t *testing.T) {
		agent := Agent{Name: "x", Detect: &DetectionCapability{Commands: []string{"ls"}}}
		avail := CheckAgentAvailabilityForAgent(agent, "/nonexistent-home", nil)
		if avail.Status != "installed" || avail.Evidence != "binary_on_path" {
			t.Errorf("got %+v, want installed/binary_on_path", avail)
		}
	})

	t.Run("marker_not_found_never_unknown", func(t *testing.T) {
		agent := Agent{Name: "x", Detect: &DetectionCapability{Commands: []string{"definitely-not-a-real-command-xyz"}}}
		avail := CheckAgentAvailabilityForAgent(agent, "/nonexistent-home", nil)
		if avail.Status != "not_installed" || avail.Evidence != "marker_not_found" {
			t.Errorf("got %+v, want not_installed/marker_not_found", avail)
		}
	})

	t.Run("no_detect_no_target_is_unknown", func(t *testing.T) {
		agent := Agent{Name: "x"}
		avail := CheckAgentAvailabilityForAgent(agent, "/nonexistent-home", nil)
		if !avail.IsUnknown() || avail.Evidence != "cannot_determine" {
			t.Errorf("got %+v, want unknown/cannot_determine", avail)
		}
	})

	t.Run("no_detect_but_target_parent_exists", func(t *testing.T) {
		dir := t.TempDir()
		target := dir + "/some/nested/file"
		agent := Agent{Name: "x"}
		avail := CheckAgentAvailabilityForAgent(agent, "/nonexistent-home", &target)
		if !avail.IsUnknown() {
			t.Errorf("got %+v, want unknown (parent %s/some/nested does not exist)", avail, dir)
		}

		target2 := dir + "/file-directly-in-tempdir"
		avail2 := CheckAgentAvailabilityForAgent(agent, "/nonexistent-home", &target2)
		if !avail2.IsInstalled() || avail2.Evidence != "target_parent_exists" {
			t.Errorf("got %+v, want installed/target_parent_exists", avail2)
		}
	})
}

func TestIsAgentInstalled(t *testing.T) {
	installed := Agent{Name: "x", Detect: &DetectionCapability{Commands: []string{"ls"}}}
	if got := IsAgentInstalled(installed, "/nonexistent-home", nil); got == nil || !*got {
		t.Errorf("IsAgentInstalled(installed) = %v, want true", got)
	}

	notInstalled := Agent{Name: "x", Detect: &DetectionCapability{Commands: []string{"definitely-not-a-real-command-xyz"}}}
	if got := IsAgentInstalled(notInstalled, "/nonexistent-home", nil); got == nil || *got {
		t.Errorf("IsAgentInstalled(not_installed) = %v, want false", got)
	}

	unknown := Agent{Name: "x"}
	if got := IsAgentInstalled(unknown, "/nonexistent-home", nil); got != nil {
		t.Errorf("IsAgentInstalled(unknown) = %v, want nil", got)
	}
}
