# Home-manager module for the cascaded user service
# Usage in home.nix:
#   imports = [ inputs.cascade.nixosModules.cascade ];
#   services.cascade.enable = true;
{ config, lib, pkgs, ... }:

let
  cfg = config.services.cascade;
in
{
  options.services.cascade = {
    enable = lib.mkEnableOption "Cascade AI context daemon (cascaded)";

    package = lib.mkOption {
      type = lib.types.package;
      description = "The cascade package to use.";
      # Default: pulled from the flake's own package output when used as an input
      default = pkgs.cascade or (throw "cascade package not found; pass it explicitly via services.cascade.package");
    };

    logLevel = lib.mkOption {
      type = lib.types.enum [ "error" "warn" "info" "debug" "trace" ];
      default = "info";
      description = "Log verbosity for cascaded (RUST_LOG level).";
    };

    extraArgs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "Extra command-line arguments passed to cascaded.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.user.services.cascaded = {
      Unit = {
        Description = "Cascade AI context daemon";
        Documentation = "https://github.com/acamarata/cascade/wiki";
        After = [ "network.target" ];
      };

      Service = {
        Type = "simple";
        ExecStart = "${cfg.package}/bin/cascaded ${lib.escapeShellArgs cfg.extraArgs}";
        Restart = "on-failure";
        RestartSec = "5s";
        Environment = [ "RUST_LOG=${cfg.logLevel}" ];
      };

      Install = {
        WantedBy = [ "default.target" ];
      };
    };
  };
}
