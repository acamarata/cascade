{
  description = "Cascade — unified AI instruction cascade manager";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, home-manager }:
    let
      # Per-system package derivation
      mkCascade = system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in import ./nix/cascade.nix { inherit pkgs system; };
    in
    flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" ] (system: {
      packages = {
        cascade = mkCascade system;
        default = mkCascade system;
      };
    }) // {
      # NixOS / home-manager module (system-independent)
      nixosModules.cascade = import ./nix/module.nix;
    };
}
