{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

{
  claude.code = {
    enable = true;

    hooks = {
      notifications = {
        enable = true;
        name = "Log Claude notifications";
        hookType = "Notification";
        command = ''notify-send -a claude -i /home/cnf/.nix-profile/share/icons/hicolor/256x256/apps/claude-desktop.png "Claude" "Sent a notification"'';
      };
      PermissionRequest = {
        enable = true;
        name = "Permission Request";
        hookType = "PermissionRequest";
        command = ''notify-send -a claude -i /home/cnf/.nix-profile/share/icons/hicolor/256x256/apps/claude-desktop.png "Claude" "Needs Permission"'';
      };
    };

    mcpServers = {
      devenv = {
        type = "stdio";
        command = "devenv";
        args = [ "mcp" ];
        env = {
          DEVENV_ROOT = config.devenv.root;
        };
      };
    };
  };

  git-hooks.hooks = {
    golangci-lint.enable = true;
    ripsecrets.enable = true;
    trufflehog.enable = true;
    gitleaks = {
      enable = true;
      entry = "${lib.getExe pkgs.gitleaks} git --pre-commit --redact --staged --verbose";
      pass_filenames = false;
    };
  };

  enterTest = ''
    go test -race ./...
    wait_for_port 8080
    http http://localhost:8080/models|jq -R -n 'inputs | try (fromjson|empty) catch input_line_number'
  '';

  env.GREET = "Arbiter";

  packages = [
    pkgs.git
    pkgs.gitleaks
    pkgs.go
    pkgs.httpie
    pkgs.air
    pkgs.gopls
    pkgs.gotools
    pkgs.golangci-lint
    pkgs.sqlc
  ];

  languages.go = {
    enable = true;
    delve.enable = true;
  };

  scripts = {
    dev.exec = "air";
    test.exec = "devenv test";
    lint.exec = "golangci-lint run";
  };

  processes.arbiter = {
    exec = "go run ./cmd/arbiter";
    ready = {
      http.get = {
        port = 8080;
        path = "/health";
        # host = "127.0.0.1";  # default
        # scheme = "http";     # default
      };
    };
  };

  tasks = {
    "arbiter:stop" = {
      exec = "devenv processes down";
      before = [ "devenv:enterTest" ];
    };
  };

}
