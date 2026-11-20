package pivotreport_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestPivotReport(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PivotReport Suite")
}
