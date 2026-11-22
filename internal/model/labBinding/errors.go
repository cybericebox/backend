package labBindingModel

import (
	"github.com/cybericebox/backend/internal/model"
	"github.com/cybericebox/backend/pkg/err"
)

var ErrLabBindingInvalid = err.ErrInvalidData.WithObjectCode(model.InfrastructureObjectCode).WithMessage("Lab binding data is invalid").WithDetailCode(2)
