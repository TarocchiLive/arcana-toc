{
  description = "Arcana ToC local music service";

  inputs.nixpkgs.url = "https://channels.nixos.org/nixos-unstable/nixexprs.tar.xz";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forEachSystem = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forEachSystem (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = pkgs.buildGoModule {
            pname = "arcana-toc";
            version = "0.1.1";
            src = pkgs.lib.cleanSource ./.;
            vendorHash = "sha256-6HX9cVzeb+s5RsoCSV6YEyg9GJU/UgA1gtqb+KL/geI=";
            subPackages = [ "cmd/arcana-toc" ];
            nativeBuildInputs = [ pkgs.pkg-config ];
            buildInputs = pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.alsa-lib ];
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              go test -race ./...
              go vet ./...
              runHook postCheck
            '';
            meta = {
              description = "Local music service for NetEase Cloud Music and QQ Music";
              license = pkgs.lib.licenses.gpl3Plus;
              mainProgram = "arcana-toc";
              platforms = systems;
            };
          };
        });

      checks = forEachSystem (system: {
        default = self.packages.${system}.default;
      });

      devShells = forEachSystem (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = pkgs.mkShell {
            inputsFrom = [ self.packages.${system}.default ];
            packages = [ pkgs.go pkgs.golangci-lint ];
          };
        });
    };
}
