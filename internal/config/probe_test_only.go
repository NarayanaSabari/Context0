package config

// Deliberately broken to prove branch protection blocks a failing check.
func ProtectionProbe() string {
	var unused int
	return "this does not compile"
}
