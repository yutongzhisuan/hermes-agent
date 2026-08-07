package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/infa/task_relay/master/agent"
)

func main() {
	cmd := newRootCommand()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "master-demo",
		Short: "Task Relay Master agent (config-driven)",
		Long: `Run the Task Relay Master agent.

Settings come from a YAML/JSON config file, with Cobra flags and
environment variables (Viper) as overrides.

Env examples:
  MASTER_CONFIG, MASTER_GOAL, MASTER_VERBOSE, MASTER_LOG_LEVEL
  OPENAI_API_KEY, OPENAI_MODEL, HUB_GRPC_ADDR, MASTER_JWT`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "version" || cmd.Name() == "help" || cmd.Name() == "completion" {
				return nil
			}
			return setupViper(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMaster(cmd, args)
		},
	}

	cmd.Flags().StringP("config", "c", "", "master config file (YAML/JSON) [MASTER_CONFIG]")
	cmd.Flags().StringP("goal", "g", "", "user goal (or pass as args) [MASTER_GOAL]")
	cmd.Flags().BoolP("verbose", "v", false, "AgentEvent trace on stderr [MASTER_VERBOSE]")
	cmd.Flags().String("log-level", "", "slog level override [MASTER_LOG_LEVEL]")
	_ = cmd.MarkFlagFilename("config", "yaml", "yml", "json")

	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("master-demo dev")
		},
	})
	return cmd
}

func runMaster(cmd *cobra.Command, args []string) error {
	_, cfg, rt, err := loadMasterFromViper(cmd)
	if err != nil {
		return err
	}

	goal := strings.TrimSpace(viper.GetString("goal"))
	if goal == "" && len(args) > 0 {
		goal = strings.Join(args, " ")
	}
	if goal == "" {
		return fmt.Errorf("goal is required (--goal/-g, args, or MASTER_GOAL)")
	}
	if (cfg.HubAddr == "") != (cfg.MasterJWT == "") {
		return fmt.Errorf("hub.grpc_addr and hub.jwt must both be set for remote mode, or both omitted for local-only mode")
	}
	if cfg.OpenAIAPIKey == "" {
		return fmt.Errorf("openai.api_key is required (config, OPENAI_API_KEY, or ${OPENAI_API_KEY})")
	}

	ctx, cancel := context.WithTimeout(context.Background(), rt.Timeout)
	defer cancel()

	master, err := agent.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("init master: %w", err)
	}
	defer master.Close()

	if master.LocalOnly {
		fmt.Fprintln(os.Stderr, "mode: local-only (no Hub / remote workers)")
	} else {
		fmt.Fprintln(os.Stderr, "mode: remote Relay via Hub")
	}
	fmt.Fprintf(os.Stderr, "config: %s\n", viper.GetString("config"))

	opts, err := buildRunOpts(rt)
	if err != nil {
		return err
	}
	answer, err := master.Run(ctx, goal, opts...)
	if err != nil {
		return fmt.Errorf("run master: %w", err)
	}
	fmt.Println(answer)
	return nil
}

func buildRunOpts(rt agent.FileRuntime) ([]agent.RunOption, error) {
	var opts []agent.RunOption
	if rt.Verbose {
		opts = append(opts, agent.WithVerbose(os.Stderr))
	}
	if strings.EqualFold(strings.TrimSpace(rt.LogLevel), "off") {
		return opts, nil
	}
	level := rt.LogLevel
	if level == "" {
		level = "info"
	}
	logger, err := agent.NewSlogLogger(os.Stderr, level, rt.LogJSON)
	if err != nil {
		return nil, err
	}
	opts = append(opts, agent.WithSlog(logger))
	return opts, nil
}
