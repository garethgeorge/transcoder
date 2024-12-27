# Transcoder

### Usage Example

```
# high quality e.g. movies 
sudo transcoder --docker-image ffmpeg --docker-cpus "0-11" --preset 6 /media/Movies
# lower quality e.g. TV shows
sudo transcoder --docker-image ffmpeg --docker-cpus "0-11" --preset 8 /media/TV
```