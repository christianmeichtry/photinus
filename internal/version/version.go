// Package version holds the release identity every lantern announces.
// It rides in memberlist's node metadata so operators can see version
// skew across the swarm during rolling upgrades.
package version

// Release is the photinus release. Bump on every published build.
const Release = "0.0.13"
