package controller

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMAPI is the subset of the SSM client the store uses.
type SSMAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, opts ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
}

// SSMStore is a ParameterStore backed by SSM Parameter Store.
type SSMStore struct {
	API SSMAPI
}

func (s SSMStore) Get(ctx context.Context, name string) (string, bool, error) {
	out, err := s.API.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)})
	var notFound *ssmtypes.ParameterNotFound
	if errors.As(err, &notFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return aws.ToString(out.Parameter.Value), true, nil
}

func (s SSMStore) Put(ctx context.Context, name, value string) error {
	_, err := s.API.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(value),
		Type:      ssmtypes.ParameterTypeString,
		Overwrite: aws.Bool(true),
		// results with findings can pass the 4KB standard tier limit.
		Tier: ssmtypes.ParameterTierIntelligentTiering,
	})
	return err
}
