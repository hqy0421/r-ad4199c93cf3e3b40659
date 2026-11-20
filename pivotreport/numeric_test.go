package pivotreport_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sclevine/agouti/pivotreport"
)

var _ = Describe("Numeric typing", func() {
	It("keeps large integers exact", func() {
		v, err := pivotreport.ParseValueForTest("9007199254740993", pivotreport.TypeInt, 0, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Kind).To(Equal(pivotreport.KindInt))
		Expect(v.Int).To(Equal("9007199254740993"))
	})

	It("rejects a decimal point in an int column rather than truncating", func() {
		_, err := pivotreport.ParseValueForTest("12.0", pivotreport.TypeInt, 0, nil)
		expectKind(err, pivotreport.KindType)
	})

	It("keeps cents exact and rejects sub-cent values", func() {
		v, err := pivotreport.ParseValueForTest("1,234.56", pivotreport.TypeCents, 0, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Cents).To(Equal("123456"))
		v, err = pivotreport.ParseValueForTest("0.05", pivotreport.TypeCents, 0, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Cents).To(Equal("5"))
		_, err = pivotreport.ParseValueForTest("0.005", pivotreport.TypeCents, 0, nil)
		expectKind(err, pivotreport.KindType)
	})

	It("distinguishes NULL from zero", func() {
		null, err := pivotreport.ParseValueForTest("NULL", pivotreport.TypeInt, 0, []string{"NULL"})
		Expect(err).NotTo(HaveOccurred())
		Expect(null.IsNull()).To(BeTrue())
		zero, err := pivotreport.ParseValueForTest("0", pivotreport.TypeInt, 0, []string{"NULL"})
		Expect(err).NotTo(HaveOccurred())
		Expect(zero.IsNull()).To(BeFalse())
		isZero, numeric := zero.IsZero()
		Expect(numeric).To(BeTrue())
		Expect(isZero).To(BeTrue())
		isZero, numeric = null.IsZero()
		Expect(numeric).To(BeFalse())
		Expect(isZero).To(BeFalse())
	})

	It("accepts accounting negatives", func() {
		v, err := pivotreport.ParseValueForTest("(1,200.50)", pivotreport.TypeCents, 0, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Cents).To(Equal("-120050"))
	})

	It("treats an empty cell as NULL, not zero", func() {
		v, err := pivotreport.ParseValueForTest("   ", pivotreport.TypeInt, 0, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.IsNull()).To(BeTrue())
	})

	It("honours a declared decimal scale and rejects excess digits", func() {
		v, err := pivotreport.ParseValueForTest("3.125", pivotreport.TypeDecimal, 3, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Decimal).To(Equal("3.125"))
		_, err = pivotreport.ParseValueForTest("3.1254", pivotreport.TypeDecimal, 3, nil)
		expectKind(err, pivotreport.KindType)
	})
})
