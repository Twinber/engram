{
  description = "Engram: Memoria persistente para agentes de IA (Nova Edition)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "1.14.5-nova";
      in
      {
        # Paquete para construir e instalar Engram
        packages.default = pkgs.buildGoModule {
          pname = "engram";
          inherit version;
          src = ./.;

          # El vendorHash cambiará cuando actualices dependencias. 
          # Usamos un hash nulo para que Nix nos diga el correcto al fallar.
          vendorHash = "sha256-O+pC4x4DKNUWr7Sx9iZOjK6a64wrQA4/lnjvkNLBX64=";

          subPackages = [ "cmd/engram" ];

          ldflags = [
            "-X main.version=${version}"
          ];

          meta = with pkgs.lib; {
            description = "Persistent memory for AI coding agents with TUI fixes";
            homepage = "https://github.com/Twinber/engram";
            license = licenses.mit;
            maintainers = [ "Twinber" ];
          };
        };

        # Entorno de desarrollo (nix develop)
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            delve
            revive
          ];

          shellHook = ''
            echo "💠 Entorno Engram (Nova Edition) cargado con éxito."
            echo "Versión de Go: $(go version | cut -d ' ' -f 3)"
            echo "Recuerda: Tus arreglos de la TUI están en este directorio."
          '';
        };
      });
}
