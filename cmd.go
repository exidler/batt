package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/distatus/battery"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/charlie0129/batt/pkg/version"
	"github.com/charlie0129/batt/smc"
)

var (
	logLevel = "info"
)

// NewCommand .
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batt",
		Short: "batt is a tool to control battery charging on Apple Silicon MacBooks",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return setupLogger()
		},
	}

	globalFlags := cmd.PersistentFlags()
	globalFlags.StringVarP(&logLevel, "log-level", "l", "info", "log level (trace, debug, info, warn, error, fatal, panic)")

	cmd.AddCommand(
		NewDaemonCommand(),
		NewVersionCommand(),
		NewInstallCommand(),
		NewUninstallCommand(),
		NewLimitCommand(),
		NewSetDisableChargingPreSleepCommand(),
		NewSetPreventIdleSleepCommand(),
		NewStatusCommand(),
		NewAdapterCommand(),
		NewLowerLimitDeltaCommand(),
	)

	return cmd
}

// NewVersionCommand .
func NewVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Printf("%s %s\n", version.Version, version.GitCommit)
		},
	}
}

// NewDaemonCommand .
func NewDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run batt daemon in the foreground",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.Infof("batt version %s commit %s", version.Version, version.GitCommit)
			runDaemon()
		},
	}
}

// NewInstallCommand .
func NewInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install batt daemon to launchd (system-wide)",
		Long: `Install batt daemon to launchd (system-wide).
This makes batt run in the background and automatically start on boot. You must run this command as root.

By default, only root user is allowed to access the batt daemon for security reasons. As a result, you will need to run batt as root to control the battery charging. If you want to allow non-root users to access the daemon, you can use the --allow-non-root-access flag. However, this is NOT recommended as it introduces security risks.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {

			err := loadConfig()
			if err != nil {
				return err
			}

			flags := cmd.Flags()
			b, err := flags.GetBool("allow-non-root-access")
			if err != nil {
				return err
			}

			if config.AllowNonRootAccess && !b {
				logrus.Warnf("Previously, non-root users were allowed to access the batt daemon. However, this will be disabled at every installation unless you provide the --allow-non-root-access flag.")
			}

			// Before installation, always reset config.AllowNonRootAccess to flag value
			// instead of the one in config file.
			config.AllowNonRootAccess = b

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if config.AllowNonRootAccess {
				logrus.Warnf("non-root users are allowed to access the batt daemon. This is NOT recommended.")
			}

			err := installDaemon()
			if err != nil {
				// check if current user is root
				if os.Geteuid() != 0 {
					logrus.Errorf("you must run this command as root")
				}
				return fmt.Errorf("failed to install daemon: %v", err)
			}

			err = saveConfig()
			if err != nil {
				return err
			}

			logrus.Infof("installation succeeded")

			exePath, _ := os.Executable()

			cmd.Printf("`launchd' will use current binary (%s) at startup so please make sure you do not move this binary. Once this binary is moved or deleted, you will need to run ``batt install'' again.\n", exePath)

			return nil
		},
	}

	cmd.Flags().Bool("allow-non-root-access", false, "Allow non-root users to access batt daemon. NOT recommended.")

	return cmd
}

// NewUninstallCommand .
func NewUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall batt daemon from launchd (system-wide)",
		Long: `Uninstall batt daemon from launchd (system-wide).
This stops batt and removes it from launchd.

You must run this command as root.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := uninstallDaemon()
			if err != nil {
				// check if current user is root
				if os.Geteuid() != 0 {
					logrus.Errorf("you must run this command as root")
				}
				return fmt.Errorf("failed to uninstall daemon: %v", err)
			}

			logrus.Infof("resetting charge limits")

			// Open Apple SMC for read/writing
			smcC := smc.New()
			if err := smcC.Open(); err != nil {
				return fmt.Errorf("failed to open SMC: %v", err)
			}

			err = smcC.EnableCharging()
			if err != nil {
				return fmt.Errorf("failed to enable charging: %v", err)
			}

			err = smcC.EnableAdapter()
			if err != nil {
				return fmt.Errorf("failed to enable adapter: %v", err)
			}

			if err := smcC.Close(); err != nil {
				return fmt.Errorf("failed to close SMC: %v", err)
			}

			fmt.Println("successfully uninstalled")

			cmd.Printf("Your config is kept in %s, in case you want to use `batt' again. If you want a complete uninstall, you can remove both config file and batt itself manually.\n", configPath)

			return nil
		},
	}
}

// NewLimitCommand .
func NewLimitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "limit [percentage]",
		Short: "Set the battery charge limit",
		Long: `Set the battery charge limit.
This is a percentage from 10 to 100.
Setting the limit to 10-99 will enable the battery charge limit. However, setting the limit to 100 will disable the battery charge limit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("invalid number of arguments")
			}

			ret, err := put("/limit", args[0])
			if err != nil {
				return fmt.Errorf("failed to set limit: %v", err)
			}

			if ret != "" {
				logrus.Infof("daemon responded: %s", ret)
			}

			logrus.Infof("successfully set battery charge limit to %s%%", args[0])

			return nil
		},
	}
}

// NewSetPreventIdleSleepCommand .
func NewSetPreventIdleSleepCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prevent-idle-sleep",
		Short: "Set whether to prevent idle sleep during a charging session",
		Long: `Set whether to prevent idle sleep during a charging session.

Due to macOS limitations, batt will pause when your computer goes to sleep. As a result, when you are in a charging session and your computer goes to sleep, the battery charge limit will no longer function and the battery will charge to 100%. If you want the battery to stay below the charge limit, this behavior is probably not what you want. This option, together with disable-charging-pre-sleep, will prevent this from happening.

To prevent this, you can set batt to prevent idle sleep. This will prevent your computer from idle sleep while in a charging session. This will only prevent **idle** sleep, when 1) charging is active 2) battery charge limit is enabled. So your computer can go to sleep as soon as a charging session is over.

However, this does not prevent manual sleep. For example, if you manually put your computer to sleep or close the lid, batt will not prevent your computer from sleeping. This is a limitation of macOS. To prevent such cases, see disable-charging-pre-sleep.`,
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "enable",
			Short: "Prevent idle sleep during a charging session",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/prevent-idle-sleep", "true")
				if err != nil {
					return fmt.Errorf("failed to set prevent idle sleep: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully enabled idle sleep prevention")

				return nil
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Do not prevent idle sleep during a charging session",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/prevent-idle-sleep", "false")
				if err != nil {
					return fmt.Errorf("failed to set prevent idle sleep: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully disabled idle sleep prevention")

				return nil
			},
		},
	)

	return cmd
}

// NewSetDisableChargingPreSleepCommand .
func NewSetDisableChargingPreSleepCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable-charging-pre-sleep",
		Short: "Set whether to disable charging before sleep if charge limit is enabled",
		Long: `Set whether to disable charging before sleep if charge limit is enabled.

Due to macOS limitations, batt will pause when your computer goes to sleep. As a result, when you are in a charging session and your computer goes to sleep, the battery charge limit will no longer function and the battery will charge to 100%. If you want the battery to stay below the charge limit, this behavior is probably not what you want. This option, together with prevent-idle-sleep, will prevent this from happening. prevent-idle-sleep can prevent idle sleep to keep the battery charge limit active. However, this does not prevent manual sleep. For example, if you manually put your computer to sleep or close the lid, batt will not prevent your computer from sleeping. This is a limitation of macOS. 

To prevent such cases, you can use disable-charging-pre-sleep. This will disable charging just before your computer goes to sleep, preventing it from charging beyond the predefined limit. Once it wakes up, batt can take over and continue to do the rest work. It will only disable charging before sleep if battery charge limit is enabled.`,
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "enable",
			Short: "Disable charging before sleep during a charging session",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/disable-charging-pre-sleep", "true")
				if err != nil {
					return fmt.Errorf("failed to set disable charging pre sleep: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully enabled disable-charging-pre-sleep")

				return nil
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Do not disable charging before sleep during a charging session",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/disable-charging-pre-sleep", "false")
				if err != nil {
					return fmt.Errorf("failed to set disable charging pre sleep: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully disabled disable-charging-pre-sleep")

				return nil
			},
		},
	)

	return cmd
}

// NewAdapterCommand .
func NewAdapterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "Enable or disable power adapter",
		Long: `Enable or disable power adapter, i.e, power input.

When you disable power adapter, power input from the wall will be disabled. This is useful when you are plugged in and you still want to consume battery instead of power input.`,
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "disable",
			Short: "Disable power adapter",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/adapter", "false")
				if err != nil {
					return fmt.Errorf("failed to disable power adapter: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully disabled power adapter")

				return nil
			},
		},
		&cobra.Command{
			Use:   "enable",
			Short: "Enable power adapter",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := put("/adapter", "true")
				if err != nil {
					return fmt.Errorf("failed to enable power adapter: %v", err)
				}

				if ret != "" {
					logrus.Infof("daemon responded: %s", ret)
				}

				logrus.Infof("successfully enabled power adapter")

				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Get the current status of power adapter",
			RunE: func(cmd *cobra.Command, args []string) error {
				ret, err := get("/adapter")
				if err != nil {
					return fmt.Errorf("failed to get power adapter status: %v", err)
				}

				switch ret {
				case "true":
					logrus.Infof("power adapter is enabled")
				case "false":
					logrus.Infof("power adapter is disabled")
				default:
					logrus.Errorf("unknown power adapter status")
				}

				return nil
			},
		},
	)

	return cmd
}

// NewStatusCommand .
func NewStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Get the current status of batt",
		RunE: func(cmd *cobra.Command, args []string) error {
			ret, err := get("/config")
			if err != nil {
				return fmt.Errorf("failed to get config: %v", err)
			}

			conf := Config{}
			err = json.Unmarshal([]byte(ret), &conf)
			if err != nil {
				return fmt.Errorf("failed to unmarshal config: %v", err)
			}
			if conf.Limit < 100 {
				cmd.Printf("charge limit: %d%%\n", conf.Limit)
				cmd.Printf("lower limit: %d%%\n", conf.Limit-conf.LowerLimitDelta)
			} else {
				cmd.Printf("charge limit: disabled\n")
			}
			cmd.Printf("prevent idle-sleep when charging: %t\n", conf.PreventIdleSleep)
			cmd.Printf("disable charging before sleep if charge limit is enabled: %t\n", conf.DisableChargingPreSleep)
			cmd.Printf("allow non-root users to assess the daemon: %t\n", conf.AllowNonRootAccess)

			cmd.Print("\n")

			ret, err = get("/battery-info")
			if err != nil {
				return fmt.Errorf("failed to get battery info: %v", err)
			}
			var bat battery.Battery
			err = json.Unmarshal([]byte(ret), &bat)
			if err != nil {
				return fmt.Errorf("failed to unmarshal battery info: %v", err)
			}
			state := "UNKNOWN"
			switch bat.State {
			case battery.Charging:
				state = "charging"
			case battery.Discharging:
				state = "discharging"
			case battery.Full:
				state = "full"
			}
			cmd.Printf("state: %s\n", state)
			cmd.Printf("full capacity: %.1f Wh\n", bat.Design/1e3)
			cmd.Printf("charge rate: %.1f W\n", bat.ChargeRate/1e3)
			cmd.Printf("voltage: %.1f V\n", bat.DesignVoltage)

			cmd.Print("\n")

			ret, err = get("/adapter")
			if err != nil {
				return fmt.Errorf("failed to get power adapter status: %v", err)
			}

			switch ret {
			case "true":
				cmd.Println("power adapter: enabled")
			case "false":
				cmd.Println("power adapter: disabled")
			default:
				cmd.Println("power adapter: unknown")
			}

			ret, err = get("/charging")
			if err != nil {
				return fmt.Errorf("failed to get charging status: %v", err)
			}

			switch ret {
			case "true":
				cmd.Println("charging: enabled")
			case "false":
				cmd.Println("charging: disabled")
			default:
				cmd.Println("charging: unknown")
			}

			return nil
		},
	}
}

// NewLowerLimitDeltaCommand .
func NewLowerLimitDeltaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lower-limit-delta",
		Short: "Set the delta between lower and upper charge limit",
		Long: `Set the delta between lower and upper charge limit.

When you set a charge limit, for example, on a Lenovo ThinkPad, you can set two percentages. The first one is the upper limit, and the second one is the lower limit. When the battery charge is above the upper limit, the computer will stop charging. When the battery charge is below the lower limit, the computer will start charging. If the battery charge is between the two limits, the computer will keep whatever charging state it is in.

batt have similar features. The charge limit you have set (using 'batt limit') will be used as the upper limit. By default, The lower limit will be set to 2% less than the upper limit. Same as using 'batt lower-limit-delta 2'. To customize the lower limit, use 'batt lower-limit-delta'.

For example, if you want to set the lower limit to be 5% less than the upper limit, run 'sudo batt lower-limit-delta 5'. By doing this, if you have your charge (upper) limit set to 60%, the lower limit will be 55%.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("invalid number of arguments")
			}

			delta, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid delta: %v", err)
			}

			ret, err := put("/lower-limit-delta", strconv.Itoa(delta))
			if err != nil {
				return fmt.Errorf("failed to set lower limit delta: %v", err)
			}

			if ret != "" {
				logrus.Infof("daemon responded: %s", ret)
			}

			logrus.Infof("successfully set lower limit delta to %d%%", delta)

			return nil
		},
	}

	return cmd
}
