package model

import "github.com/cybericebox/backend/pkg/err"

var ErrPlatform = err.ErrInternal.WithObjectCode(PlatformObjectCode)
