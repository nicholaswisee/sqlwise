package access

import (
	"errors"
)

var ErrInvalidPermission = errors.New("invalid permission")

type Permission uint8

const (
	None Permission = iota
	View
	Edit
)

func (p Permission) String() string {
	switch p {
	case None:
		return "none"
	case View:
		return "view"
	case Edit:
		return "edit"
	default:
		return "invalid"
	}
}

func ParsePermission(s string) (Permission, error) {
	switch s {
	case "none":
		return None, nil
	case "view":
		return View, nil
	case "edit":
		return Edit, nil
	default:
		return None, ErrInvalidPermission
	}
}
