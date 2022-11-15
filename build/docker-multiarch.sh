TAG="ruakij/routingtabletowg"
PLATFORM="linux/amd64,linux/arm64/v8,linux/arm/v7"
EXTRA_ARGS="$@"

docker buildx build \
--platform $PLATFORM \
--tag $TAG \
$EXTRA_ARGS \
.
