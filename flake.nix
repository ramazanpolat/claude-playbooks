{
  description = "claude-playbook (cpb): install, isolate and launch Claude Code playbooks";

  # For devbox and Nix users:
  #
  #   devbox add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#claude-playbook"
  #   nix run   "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#claude-playbook" -- --version
  #
  # git+https, not github: -- a github: ref is resolved through GitHub's API,
  # which rate-limits unauthenticated callers per IP (403 behind a shared IP,
  # through devbox as well). git+https makes no API call and pins the commit.
  #
  # Built from source at the pinned ref, never from release binaries: a tagged
  # commit cannot carry the hashes of binaries built after it was tagged. Pin a
  # release TAG -- the version reported is package.json's, which release.yml
  # refuses to tag unless it equals the tag, so between releases a commit
  # reports the last release's version.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = (builtins.fromJSON (builtins.readFile ./package.json)).version;
    in
    {
      packages = forAllSystems (pkgs: rec {
        claude-playbook = pkgs.buildGoModule {
          pname = "claude-playbook";
          inherit version;
          src = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum ./main.go ./cmd ./internal ];
          };
          # Recompute after any go.mod/go.sum change: set pkgs.lib.fakeHash,
          # build, and copy the hash the error reports.
          vendorHash = "sha256-FhX6eKtdxb7QaRdmYlFf1NfpxbasB5YQ1YNFRKwpof8=";
          subPackages = [ "." ];
          # One static binary with no runtime closure: which nixpkgs built it
          # then only matters at build time, never in a user's profile.
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=v${version}" ];
          # The Go suite runs in CI on both OSes; inside the Nix sandbox it has
          # no HOME, no git and no network, which several tests need.
          doCheck = false;
          # The module is claude-playbooks; the command is claude-playbook,
          # with cpb beside it exactly as install.sh lays them out.
          postInstall = ''
            mv $out/bin/claude-playbooks $out/bin/claude-playbook
            ln -s claude-playbook $out/bin/cpb
          '';
          meta = {
            description = "Install, isolate and launch Claude Code playbooks";
            homepage = "https://github.com/ramazanpolat/claude-playbooks";
            mainProgram = "claude-playbook";
          };
        };
        default = claude-playbook;
      });
    };
}
