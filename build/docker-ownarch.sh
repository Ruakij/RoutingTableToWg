TAG="ruakij/routingtabletowg"
EXTRA_ARGS="$@"

docker build \
--tag $TAG \
$EXTRA_ARGS \
.
