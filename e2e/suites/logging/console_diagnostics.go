package logging

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/eks-node-monitoring-agent/pkg/util/validation"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const latestConsoleOutputUnsupportedMsg = "UnsupportedOperation: This instance type does not support retrieving the latest console logs"

func ConsoleLogging(awsCfg aws.Config) types.Feature {
	ec2Client := ec2.NewFromConfig(awsCfg)

	return features.New("ConsoleLogging").
		Assess("ReadConsoleOutput", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var nodeList corev1.NodeList
			if err := cfg.Client().Resources().List(ctx, &nodeList); err != nil {
				t.Fatal(err)
			}
			targetNode := &nodeList.Items[0]
			t.Logf("targetting node %q for test", targetNode.Name)

			// We can't rely on nodeName yet in order to get instanceId, so
			// parse the provider ID in order to find the instance id.
			instanceId, err := validation.ParseProviderID(targetNode.Spec.ProviderID)
			if err != nil {
				t.Fatal(err)
			}

			var getConsoleOutputResponse *ec2.GetConsoleOutputOutput
			for i := 0; i < 10; i++ {
				getConsoleOutputResponse, err = ec2Client.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{
					InstanceId: &instanceId,
					Latest:     aws.Bool(true),
				})
				if err != nil {
					if strings.Contains(err.Error(), latestConsoleOutputUnsupportedMsg) {
						t.Skipf("skipping instance type that does not support latest console output operation")
					}
					t.Fatal(err)
				}
				// if there is no output, wait and try again
				if len(*getConsoleOutputResponse.Output) > 1024 {
					break
				}
				time.Sleep(10 * time.Second)
			}

			consoleOutputBytes, err := base64.StdEncoding.DecodeString(*getConsoleOutputResponse.Output)
			if err != nil {
				t.Fatal(err)
			}
			consoleOutput := string(consoleOutputBytes)

			t.Log("checking that all sections are visible in the ec2 console output...")
			var sectionChecklist = map[string]bool{
				"cpu":        false,
				"memory":     false,
				"disk":       false,
				"interfaces": false,
				"apiserver":  false,
				"dmesg":      false,
				"systemd":    false,
				"kubelet":    false,
				"containerd": false,
				"ipamd":      false,
			}
			const nmaLogSectionHeader = "NMA::LOG"
			for _, consoleLine := range strings.Split(consoleOutput, "\n") {
				consoleLine = strings.TrimSpace(consoleLine)
				if strings.Contains(consoleLine, nmaLogSectionHeader) {
					headerParts := strings.FieldsFunc(consoleLine, func(r rune) bool { return r == '|' })
					if len(headerParts) != 3 {
						t.Errorf("incorrectly formatted header: %q", consoleLine)
						continue
					}
					producerName := headerParts[2]
					if _, ok := sectionChecklist[producerName]; !ok {
						t.Errorf("unknown log producer: %q", producerName)
						continue
					}
					sectionChecklist[producerName] = true
				}
			}
			for sectionProducer, found := range sectionChecklist {
				if !found {
					t.Errorf("❌ didn't find section for: %q", sectionProducer)
				} else {
					t.Logf("✅ found section for: %q", sectionProducer)
				}
			}

			// log the entire output if something goes wrong that way we can see
			// exactly why the test case failed.
			if t.Failed() {
				t.Logf("console-output:\n%s", consoleOutput)
			}

			return ctx
		}).
		Feature()
}
