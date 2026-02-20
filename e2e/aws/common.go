package aws

const (
	StageBeta = "beta"
	StageProd = "prod"
)

func GetEksEndpoint(stage, region string) string {
	// TODO: handle more cases if they become necessary
	if stage == StageBeta {
		return "https://api.beta.us-west-2.wesley.amazonaws.com"
	}
	return ""
}
