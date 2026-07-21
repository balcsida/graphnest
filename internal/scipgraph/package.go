package scipgraph

import (
	"errors"
	"net/url"
	"strings"
)

var errInvalidPackageURL = errors.New("invalid package URL")

type Package struct {
	PURL, Manager, Name, Version string
}

type PackageMapping struct {
	Package  Package
	Relation string
	Source   string
}

func ParsePackageURL(value string) (Package, error) {
	scheme, rest, ok := strings.Cut(value, ":")
	if !ok || scheme != "pkg" || strings.ContainsAny(rest, "?#") {
		return Package{}, errInvalidPackageURL
	}
	packageType, nameVersion, ok := strings.Cut(rest, "/")
	if !ok {
		return Package{}, errInvalidPackageURL
	}
	packageType = strings.ToLower(packageType)
	manager, ok := map[string]string{
		"golang": "gomod", "npm": "npm", "maven": "maven", "pypi": "pip",
		"cargo": "cargo", "nuget": "nuget", "gem": "gem",
	}[packageType]
	separator := strings.LastIndexByte(nameVersion, '@')
	if !ok || packageType == "" || separator <= 0 || separator == len(nameVersion)-1 {
		return Package{}, errInvalidPackageURL
	}
	name, err := url.PathUnescape(nameVersion[:separator])
	if err != nil || name == "" {
		return Package{}, errInvalidPackageURL
	}
	version, err := url.PathUnescape(nameVersion[separator+1:])
	if err != nil || version == "" {
		return Package{}, errInvalidPackageURL
	}
	return Package{
		PURL:    "pkg:" + packageType + "/" + name + "@" + version,
		Manager: manager, Name: name, Version: version,
	}, nil
}
