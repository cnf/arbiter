{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

{
  env.GREET = "Arbiter";
  env.LITELLM_URL = config.secretspec.secrets.LITELLM_URL or "";
  env.LITELLM_API_KEY = config.secretspec.secrets.LITELLM_API_KEY or "";

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

  packages = [
    pkgs.git
    pkgs.gitleaks
    pkgs.httpie
    pkgs.go
    pkgs.air
    pkgs.gopls
    pkgs.gotools
    pkgs.golangci-lint
    pkgs.logdy
  ];

  languages.go = {
    enable = true;
    delve.enable = true;
  };

  languages.python = {
    enable = true;
    venv = {
      enable = true;
      requirements = ''
        fakellm
      '';
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
  '';


  scripts = {
    docs.exec = "go doc -http";
    dev.exec = "air";
    test.exec = "devenv test";
    lint.exec = "golangci-lint run";
    mock.exec = ''
      http --check-status -S POST :5665/chat/completions model="mock-llm" messages[0]["role"]="user" messages[0]["content"]="what color is the sky?" stream:=true
      http --check-status -S POST :8080/chat/completions model="mock-llm" messages[0]["role"]="user" messages[0]["content"]="what color is the sky?" stream:=true

    '';
  };


  processes.arbiter = {
    ports.logdy.allocate = 8312;
    ports.http.allocate = 8080;
    exec = "secretspec run -- go run ./cmd/arbiter --port ${toString config.processes.arbiter.ports.http.value} |logdy --no-analytics -t -p ${toString config.processes.arbiter.ports.logdy.value}";
    ready = {
      http.get = {
        port = config.processes.arbiter.ports.http.value;
        path = "/health";
        # host = "127.0.0.1";  # default
        # scheme = "http";     # default
      };
    };
  };

  processes.mockllm = {
    ports.openai.allocate = 5665;
    exec = "fakellm serve --port 5665 --config support/fakellm.yaml";
    before = ["devenv:processes:arbiter"];
#    ready = {
#      http.get = {
#        port = 5665;
#        path = "/health";
#        # host = "127.0.0.1";  # default
#        # scheme = "http";     # default
#      };
#    };
  };

  tasks = {
    "arbiter:stop" = {
      exec = "devenv processes down";
      before = [ "devenv:enterTest" ];
    };
  };

  ## mockllm
  
}
