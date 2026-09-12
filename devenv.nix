{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

{
  env.GREET = "Arbiter";

  packages = [
    pkgs.git
    pkgs.go
    pkgs.air
    pkgs.gopls
    pkgs.gotools
    pkgs.golangci-lint
    pkgs.sqlc
  ];

  languages.go.enable = true;

  scripts = {
    dev.exec = "air";
    test.exec = "go test ./...";
    lint.exec = "golangci-lint run";
  };
}
