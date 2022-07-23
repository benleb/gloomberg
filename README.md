# gloomberg

## requirements

- get your wallet address or ens
  ***yourDegenWallet.eth*** or ***0x9e7DC5307940fa170F9093Ca548bDa0EDB602767***
- get an account at [Infura](https://infura.io)/[Alchemy](https://www.alchemy.com)/*whatever* to get a websockets endpoint to an ethereum node
  ***wss://mainnet.infura.io/ws/v3/32e98f6ffb81456df24087ab5b***

## quickstart

```bash
# get link to latest linux amd64 binary
GBL=$(curl -L -s -H 'Accept: application/json' https://github.com/benleb/gloomberg/releases/latest | sed -e 's/.*"tag_name":"\([^"]*\)".*/\1/')
# download binary and extract it to /usr/local/bin
wget -qO- https://github.com/benleb/gloomberg/releases/download/${GBL}/gloomberg_${GBL/v/}_linux_amd64.tar.gz | sudo tar -C /usr/local/bin -vzx gloomberg

# run
gloomberg live -e "wss://mainnet.infura.io/ws/v3/32e9..." -w "yourDegenWallet.eth"
```

## docker

```shell
docker run --rm -it \
  --env "GLOOMBERG_ENDPOINTS=wss://mainnet.infura.io/ws/v3/32e9..."
  --env "GLOOMBERG_WALLETS=yourDegenWallet.eth"
  ghcr.io/benleb/gloomberg:latest live
```
