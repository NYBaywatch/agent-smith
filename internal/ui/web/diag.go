package web

import (
	"github.com/NYBaywatch/agent-smith/internal/classifier"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
)

// pathmonDiag reads a traceroute with the classifier's thresholds.
func pathmonDiag(p pathmon.Path) pathmon.Diagnosis {
	return pathmon.FirstDegradedHop(p, classifier.PathLossThreshold, classifier.PathJumpThreshold)
}
