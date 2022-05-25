package main

import (
	"errors"

	"github.com/google/licenseclassifier"
)

var (
	ErrUnknownLicense = errors.New("unknown license")
)

type LicenseType int

const (
	LicenseUnknown LicenseType = iota
	LicenseApache20
	LicenseMIT
	LicenseBSD3Clause
	LicenseISC
	LicenseOther

	defaultConfidenceTreshHold = 0.9
)

// getLicenseType takes a license name and returns the equivalent
// LicenseType constant
func getLicenseType(name string) LicenseType {
	switch name {
	case "Apache-2.0":
		return LicenseApache20
	case "MIT":
		return LicenseMIT
	case "BSD-3-Clause":
		return LicenseMIT
	case "ISC":
		return LicenseISC
	default:
		return LicenseOther
	}
}

// License represents a sotfware distribution and usage license
type License struct {
	// The name of the license
	Name string
	// If the name of the license if unkown, this field will be
	// filled
	Data []byte
}

// newLicenseClassifier instantiate a new classifer and returns
// a licenseClassifierWrapper
func newLicenseClassifier(
	confidenceThreshold float64,
) (*licenseClassifierWrapper, error) {
	classifier, err := licenseclassifier.New(confidenceThreshold)
	if err != nil {
		return nil, err
	}

	return &licenseClassifierWrapper{
		classifier: classifier,
	}, nil
}

// licenseClassifierWrapper is a wrapper around licenseclassifier.License
type licenseClassifierWrapper struct {
	classifier *licenseclassifier.License
}

// detectLicense takes the byte of a given license and returns its name
func (lcw *licenseClassifierWrapper) detectLicense(data []byte) (string, error) {
	matches := lcw.classifier.MultipleMatch(string(data), true)
	if len(matches) == 0 {
		return "", ErrUnknownLicense
	}

	licenseName := matches[0].Name
	return licenseName, nil
}
