package projectsync

// ValidateStateStoreRoot is skill_state.py's validate_state_store_root
// without creating the directory, for read-only inspection (doctor).
func ValidateStateStoreRoot(home string) (string, string) {
	return validateStateStoreRoot(home, false)
}
