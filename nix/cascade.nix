{ pkgs, system ? "x86_64-linux" }:

let
  version = "1.0.0";

  srcs = {
    "x86_64-linux" = {
      url = "https://github.com/acamarata/cascade/releases/download/v${version}/cascade_${version}_linux_x86_64.tar.gz";
      sha256 = "PLACEHOLDER_SHA256"; # updated by release automation on each publish
    };
    "aarch64-linux" = {
      url = "https://github.com/acamarata/cascade/releases/download/v${version}/cascade_${version}_linux_aarch64.tar.gz";
      sha256 = "PLACEHOLDER_SHA256";
    };
  };

  src = srcs.${system} or (throw "cascade: unsupported system ${system}");
in

pkgs.stdenv.mkDerivation rec {
  pname = "cascade";
  inherit version;

  src = pkgs.fetchurl {
    url = src.url;
    sha256 = src.sha256;
  };

  buildInputs = with pkgs; [
    glib
    gtk3
    webkitgtk_4_1
  ];

  nativeBuildInputs = with pkgs; [
    autoPatchelfHook
    makeWrapper
  ];

  # Pre-built binary — no build step
  dontBuild = true;

  installPhase = ''
    runHook preInstall

    install -Dm755 cascade   $out/bin/cascade
    install -Dm755 cascaded  $out/bin/cascaded

    # Wrap binaries so runtime GTK/WebKit libraries are found
    wrapProgram $out/bin/cascade \
      --prefix LD_LIBRARY_PATH : "${pkgs.lib.makeLibraryPath buildInputs}"

    wrapProgram $out/bin/cascaded \
      --prefix LD_LIBRARY_PATH : "${pkgs.lib.makeLibraryPath buildInputs}"

    runHook postInstall
  '';

  meta = with pkgs.lib; {
    description = "Unified AI instruction cascade manager for coding agents";
    longDescription = ''
      Cascade consolidates AI tool instruction files (CLAUDE.md, AGENTS.md, .cursorrules)
      into a single tiered hierarchy that every tool reads automatically.
    '';
    homepage = "https://github.com/acamarata/cascade";
    license = licenses.mit;
    maintainers = [ { name = "Aric Camarata"; email = "aric.camarata@gmail.com"; } ];
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "cascade";
  };
}
